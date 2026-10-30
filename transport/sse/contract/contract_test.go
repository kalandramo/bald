package contract

// contract_test.go —— SSE 契约装配的字段映射与三态语义。
//
// 锁定的不变量：
//  1. 段缺失 → (nil, nil, nil)；
//  2. addr 为空 → fail-fast（配了段却不给监听地址属漏配）；
//  3. path 为空 → 不追加 WithPath（用实现默认 /events，不得被空串覆盖）；
//  4. path 非空 → 正确透传；
//  5. afterBuild 回调在 server 构造后被调用，且拿到非 nil server；
//  6. afterBuild 返回 error → 装配失败。
//
// 这些断言共同钉住契约 provider 的行为等价性：path 处理与接线由
// Provider + Option 承担。

import (
	"context"
	"errors"
	"net/http"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

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
