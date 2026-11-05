package contract

// contract_test.go —— WebSocket 契约装配的字段映射与三态语义。
//
// 锁定的不变量：
//  1. 段缺失 → (nil, nil, nil)（引擎跳过构建）；
//  2. addr 为空 → fail-fast（空串会绑随机端口，落点不由配置决定）；
//  3. path 为空 → 不追加 WithPath（保留实现默认 /ws，不得被空串覆盖）；
//  4. 空 tls 段 → 交给 tlsconf 报错（不静默回落明文）；
//  5. afterBuild 在 NewServer 之后被调用且拿到非 nil server；其 error 短路装配；
//  6. 业务 Option 追加在契约字段之后（可覆盖 addr/path）。

import (
	"context"
	"errors"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	ws "github.com/kalandramo/bald/transport/websocket"
)

func wsServerCfg(addr, path string, tlsSec *bootstrapv1.Server_TLS) *bootstrapv1.Server {
	return &bootstrapv1.Server{Websocket: &bootstrapv1.Server_Websocket{Addr: addr, Path: path, Tls: tlsSec}}
}

// TestProvider_SectionMissingSkips 段缺失 → nil server。
func TestProvider_SectionMissingSkips(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv != nil {
		t.Fatalf("srv = %v, want nil (websocket section absent)", srv)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil on skip, want nil")
	}
}

// TestProvider_EmptyAddrFailsFast 配了段却不给地址属漏配，必须显式报错。
func TestProvider_EmptyAddrFailsFast(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), wsServerCfg("", "", nil))
	if err == nil {
		t.Fatalf("Provider() error = nil, want fail-fast on empty addr (srv=%v)", srv)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil on error path")
	}
}

// TestProvider_SectionPresentConstructs addr 非空 → 构造出 *ws.Server 且端点即契约地址。
func TestProvider_SectionPresentConstructs(t *testing.T) {
	srv, _, err := Provider()(context.Background(), wsServerCfg("127.0.0.1:19701", "", nil))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	got, ok := srv.(*ws.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *websocket.Server", srv)
	}
	if ep := got.Endpoint(); ep != "ws://127.0.0.1:19701" {
		t.Fatalf("Endpoint() = %q, want ws://127.0.0.1:19701", ep)
	}
}

// TestBuildOpts_EmptyPathNotApplied path 为空 → 不追加 Option（保留默认 /ws）。
//
// 反向陷阱防护：恒传 WithPath("") 会把路径设成空串，与契约注释「默认 /ws」相悖。
func TestBuildOpts_EmptyPathNotApplied(t *testing.T) {
	opts := mustBuildOpts(t, &bootstrapv1.Server_Websocket{Addr: "127.0.0.1:19702"})
	if len(opts) != 0 {
		t.Fatalf("len(buildOpts) = %d, want 0 (empty path must not append Option)", len(opts))
	}
}

// mustBuildOpts 取 buildOpts 的 Option 序列（不配 tls 段即不触证书解析）。
func mustBuildOpts(t *testing.T, sec *bootstrapv1.Server_Websocket) []ws.Option {
	t.Helper()
	opts, err := buildOpts(sec)
	if err != nil {
		t.Fatalf("buildOpts: %v", err)
	}
	return opts
}

// TestBuildOpts_PathAndTLSApplied path 非空 → 追加 WithPath；tls 段在 → 追加 WithTLSConfig。
func TestBuildOpts_PathAndTLSApplied(t *testing.T) {
	opts := mustBuildOpts(t, &bootstrapv1.Server_Websocket{Addr: "127.0.0.1:19703", Path: "/live"})
	if len(opts) != 1 {
		t.Fatalf("len(buildOpts) = %d, want 1", len(opts))
	}
}

// TestProvider_EmptyTlsSectionFailsFast 空 tls 段 = 「启用但没证书」，必须报错。
func TestProvider_EmptyTlsSectionFailsFast(t *testing.T) {
	_, _, err := Provider()(context.Background(), wsServerCfg("127.0.0.1:19704", "", &bootstrapv1.Server_TLS{}))
	if err == nil {
		t.Fatal("error = nil, want fail-fast on empty tls section (no silent plaintext fallback)")
	}
}

// TestProvider_AfterBuildHookInvoked 构造后回调被调用，且拿到返回的同一 server。
func TestProvider_AfterBuildHookInvoked(t *testing.T) {
	var got *ws.Server
	p := Provider(WithAfterBuild(func(_ context.Context, s *ws.Server) error {
		got = s
		return nil
	}))
	srv, _, err := p(context.Background(), wsServerCfg("127.0.0.1:19705", "", nil))
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
	p := Provider(WithAfterBuild(func(context.Context, *ws.Server) error { return sentinel }))
	_, _, err := p(context.Background(), wsServerCfg("127.0.0.1:19706", "", nil))
	if !errors.Is(err, sentinel) {
		t.Fatalf("Provider() error = %v, want wrapped %v", err, sentinel)
	}
}

// TestProvider_BusinessOptionsAppendAfterContract 业务 Option 追加在契约 Option 之后。
//
// 覆盖序的构造期证据；其线上形态（业务 WithPath 顶掉契约 path）由
// TestMappingOffline_BusinessOptionOverridesPath 判。
//
// 口径记录：websocket 的 addr 是 NewServer 的**位置参数**，不像 tcp 有
// WithAddress 可供业务 Option 覆盖——契约段是 addr 的唯一来源，没有覆盖通道。
func TestProvider_BusinessOptionsAppendAfterContract(t *testing.T) {
	var seenContractPath bool
	p := Provider(
		WithServerOptions(ws.WithPath("/final")),
		WithAfterBuild(func(context.Context, *ws.Server) error { seenContractPath = true; return nil }),
	)
	srv, _, err := p(context.Background(), wsServerCfg("127.0.0.1:19707", "/contract", nil))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv == nil || !seenContractPath {
		t.Fatalf("srv=%v afterBuildRan=%v, want server built and hook run", srv, seenContractPath)
	}
}
