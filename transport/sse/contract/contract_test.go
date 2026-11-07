package contract

// contract_test.go —— SSE 契约装配的字段映射与三态语义。
//
// 锁定的不变量：
//  1. 段缺失 → (nil, nil, nil)；
//  2. addr 为空 → fail-fast（配了段却不给监听地址属漏配）；
//  3. path 为空 → 不追加 WithPath（用实现默认 /events，不得被空串覆盖）；
//  4. path 非空 → 正确透传；
//  5. afterBuild 回调在 server 构造后被调用，且拿到非 nil server；
//  6. afterBuild 返回 error → 装配失败；
//  7. auto_stream / auto_replay 是 proto3 optional：不表态保留实现默认，
//     显式 true/false 都映射到 Option（presence 语义，不是真假语义）；
//  8. 两个字段的运行期行为可观测：auto_stream 决定未知流是否自动创建，
//     auto_replay 决定带 Last-Event-ID 的重连是否回放历史（实现默认开启）。
//
// 这些断言共同钉住契约 provider 的行为等价性：字段处理与接线由
// Provider + Option 承担。

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"google.golang.org/protobuf/proto"

	"github.com/kalandramo/bald/transport/sse"
)

// TestProvider_SectionMissingSkips 段缺失 → nil server（引擎跳过构建）。
func TestProvider_SectionMissingSkips(t *testing.T) {
	p := Provider()
	srv, cleanup, err := p(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv != nil {
		t.Fatalf("srv = %v, want nil (sse section absent)", srv)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil on skip, want nil")
	}
}

// TestProvider_EmptyAddrFailsFast 段存在但 addr 为空 → fail-fast。
//
// 与旧逃生舱语义相反（那里 addr 空 = 不启用）；现在「不启用」的正确表达是
// 移除 sse 段，故 addr 空属配置漏配，必须显式报错而非静默跳过。
func TestProvider_EmptyAddrFailsFast(t *testing.T) {
	p := Provider()
	cfg := &bootstrapv1.Server{Sse: &bootstrapv1.Server_Sse{}}
	srv, cleanup, err := p(context.Background(), cfg)
	if err == nil {
		t.Fatalf("Provider() error = nil, want fail-fast on empty addr (srv=%v)", srv)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil on error path")
	}
}

// TestProvider_SectionPresentConstructs 段存在且 addr 非空 → 构造出 *sse.Server。
func TestProvider_SectionPresentConstructs(t *testing.T) {
	p := Provider()
	cfg := &bootstrapv1.Server{Sse: &bootstrapv1.Server_Sse{Addr: ":18090"}}
	srv, _, err := p(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if _, ok := srv.(*sse.Server); !ok {
		t.Fatalf("srv type = %T, want *sse.Server", srv)
	}
}

// TestBuildOpts_EmptyPathNotApplied path 为空 → 不追加 Option（保留实现默认 /events）。
//
// 反向陷阱防护：若恒传 WithPath("")，空段 sse: {} 会把路径设成空串，
// 与契约注释「默认 /events」相悖。
func TestBuildOpts_EmptyPathNotApplied(t *testing.T) {
	opts := buildOpts(&bootstrapv1.Server_Sse{})
	if len(opts) != 0 {
		t.Fatalf("len(buildOpts) = %d, want 0 (empty path must not append Option)", len(opts))
	}
}

// TestBuildOpts_PathTransferred path 非空 → 追加一个 Option。
func TestBuildOpts_PathTransferred(t *testing.T) {
	opts := buildOpts(&bootstrapv1.Server_Sse{Path: "/custom-events"})
	if len(opts) != 1 {
		t.Fatalf("len(buildOpts) = %d, want 1", len(opts))
	}
}

// TestBuildOpts_BoolPresenceSemantics 两个 optional bool 按 **presence** 映射。
//
// 反例是常见的「按真假映射」：`if sec.GetAutoReplay()` 会让显式 false 与不表态
// 混为一谈，而 auto_replay 的实现默认是开启——那样契约就永远关不掉重放。
//
// 对 auto_stream，显式 false 与不表态今天行为相同（实现默认也是 false），所以
// 运行期判据分辨不了这一格；presence 由本单元判据守住，这样实现默认将来若改成
// true，契约侧仍能表达 false。
func TestBuildOpts_BoolPresenceSemantics(t *testing.T) {
	cases := []struct {
		name string
		sec  *bootstrapv1.Server_Sse
		want int
	}{
		{"both unset → no Options", &bootstrapv1.Server_Sse{}, 0},
		{"auto_stream explicit false still maps", &bootstrapv1.Server_Sse{AutoStream: proto.Bool(false)}, 1},
		{"auto_replay explicit false still maps", &bootstrapv1.Server_Sse{AutoReplay: proto.Bool(false)}, 1},
		{"both set → two Options", &bootstrapv1.Server_Sse{AutoStream: proto.Bool(true), AutoReplay: proto.Bool(false)}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(buildOpts(tc.sec)); got != tc.want {
				t.Fatalf("len(buildOpts) = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestProvider_AutoStreamRuntime auto_stream 的三态在真服务器上可观测：
// 未知流是否自动创建、请求是否 200。
func TestProvider_AutoStreamRuntime(t *testing.T) {
	cases := []struct {
		name  string
		value *bool
		want  bool
	}{
		{"unset keeps implementation default (off)", nil, false},
		{"explicit true auto-creates", proto.Bool(true), true},
		{"explicit false stays off", proto.Bool(false), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := startContractServer(t, &bootstrapv1.Server_Sse{
				Addr:       "127.0.0.1:0",
				AutoStream: tc.value,
			})

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.Endpoint()+"/events?stream=probe", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET /events: %v", err)
			}
			resp.Body.Close()

			if tc.want {
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status = %d, want 200 (auto_stream on)", resp.StatusCode)
				}
				if srv.GetStream("probe") == nil {
					t.Fatal("stream was not auto-created")
				}
				return
			}
			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 (unknown stream, auto_stream off)", resp.StatusCode)
			}
			if srv.GetStream("probe") != nil {
				t.Fatal("stream must not be created when auto_stream is off")
			}
		})
	}
}

// TestProvider_AutoReplayRuntime auto_replay 的三态在真服务器上可观测：
// 带 Last-Event-ID 的重连是否拿到历史事件。实现默认是开启，所以这条判据
// 必须同时钉住「不表态＝开」与「显式 false＝关」。
func TestProvider_AutoReplayRuntime(t *testing.T) {
	cases := []struct {
		name  string
		value *bool
		want  bool
	}{
		{"unset keeps implementation default (on)", nil, true},
		{"explicit false disables replay", proto.Bool(false), false},
		{"explicit true enables replay", proto.Bool(true), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := startContractServer(t, &bootstrapv1.Server_Sse{
				Addr:       "127.0.0.1:0",
				AutoReplay: tc.value,
			})
			srv.CreateStream("history")

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// 先挂一个订阅者：事件只有在事件循环里处理过（auto_replay 开启时
			// 同时写进 eventLog）之后才会投递给它，读到这里即证明「已处理」。
			live := openStream(t, ctx, srv.Endpoint(), "history")
			srv.Publish(ctx, "history", &sse.Event{Data: []byte("history-payload")})
			if got := readDataLine(t, live.Body, 2*time.Second); got != "history-payload" {
				t.Fatalf("live subscriber got %q, want %q", got, "history-payload")
			}
			live.Body.Close()

			// 重连者不带 Last-Event-ID（= 从头回放）：开启时拿得到历史，
			// 关闭时拿不到任何 data 行。
			replay := openStreamWithLastEventID(t, ctx, srv.Endpoint(), "history", "")
			defer replay.Body.Close()
			got, ok := tryReadDataLine(replay.Body, 300*time.Millisecond)
			if !tc.want {
				if ok {
					t.Fatalf("replayed %q with auto_replay off", got)
				}
				return
			}
			if !ok {
				t.Fatal("no replayed data line within 300ms with auto_replay on")
			}
			if got != "history-payload" {
				t.Fatalf("replayed data = %q, want %q", got, "history-payload")
			}
		})
	}
}

// startContractServer 经 Provider 构造并启动服务器，等待监听地址就绪。
func startContractServer(t *testing.T, sec *bootstrapv1.Server_Sse) *sse.Server {
	t.Helper()
	raw, cleanup, err := Provider()(context.Background(), &bootstrapv1.Server{Sse: sec})
	if err != nil {
		t.Fatalf("Provider() error = %v", err)
	}
	if cleanup != nil {
		t.Fatalf("cleanup != nil, want nil")
	}
	srv, ok := raw.(*sse.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *sse.Server", raw)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Start(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ep := srv.Endpoint(); !strings.HasSuffix(ep, ":0") {
			return srv
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("server did not bind: Endpoint() = %q", srv.Endpoint())
	return nil
}

// openStream 订阅已有流并读到响应头（= 已注册为订阅者）。
func openStream(t *testing.T, ctx context.Context, endpoint, stream string) *http.Response {
	t.Helper()
	return openStreamWithLastEventID(t, ctx, endpoint, stream, "")
}

func openStreamWithLastEventID(t *testing.T, ctx context.Context, endpoint, stream, lastEventID string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/events?stream="+stream, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events?stream=%s: %v", stream, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	return resp
}

// readDataLine 读下一条 data 行；超时即判红。
func readDataLine(t *testing.T, r io.Reader, timeout time.Duration) string {
	t.Helper()
	line, ok := tryReadDataLine(r, timeout)
	if !ok {
		t.Fatalf("no SSE data line within %v", timeout)
	}
	return line
}

// tryReadDataLine 读下一条 data 行；超时返回 ok=false（用于 absence 断言）。
func tryReadDataLine(r io.Reader, timeout time.Duration) (string, bool) {
	ch := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "data:") {
				ch <- strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				return
			}
		}
	}()
	select {
	case v := <-ch:
		return v, true
	case <-time.After(timeout):
		return "", false
	}
}

// TestProvider_AfterBuildHookInvoked 构造后回调被调用，且拿到非 nil server。
func TestProvider_AfterBuildHookInvoked(t *testing.T) {
	var got *sse.Server
	p := Provider(WithAfterBuild(func(_ context.Context, s *sse.Server) error {
		got = s
		return nil
	}))
	cfg := &bootstrapv1.Server{Sse: &bootstrapv1.Server_Sse{Addr: ":18091"}}
	srv, _, err := p(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if got == nil {
		t.Fatal("afterBuild hook not invoked")
	}
	if any(got) != any(srv) {
		t.Fatal("hook received a different server than the one returned")
	}
}

// TestProvider_AfterBuildErrorPropagates 回调返回 error → 装配失败。
func TestProvider_AfterBuildErrorPropagates(t *testing.T) {
	sentinel := errors.New("wiring failed")
	p := Provider(WithAfterBuild(func(context.Context, *sse.Server) error {
		return sentinel
	}))
	cfg := &bootstrapv1.Server{Sse: &bootstrapv1.Server_Sse{Addr: ":18092"}}
	_, _, err := p(context.Background(), cfg)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Provider() error = %v, want wrapped %v", err, sentinel)
	}
}

// TestProvider_ExtraServerOptionsAppend 业务 Option 追加在契约字段之后。
//
// 用于确认「函数注入」通道可用（授权钩子/订阅回调整经此传入）——
// 这是 SSE 不必再走逃生舱的核心论据。
func TestProvider_ExtraServerOptionsAppend(t *testing.T) {
	authorize := func(_ *http.Request, _ string) error { return nil }
	p := Provider(WithServerOptions(sse.WithAuthorizeFunc(authorize)))
	cfg := &bootstrapv1.Server{Sse: &bootstrapv1.Server_Sse{Addr: ":18093", Path: "/e"}}
	srv, _, err := p(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv == nil {
		t.Fatal("srv = nil")
	}
}
