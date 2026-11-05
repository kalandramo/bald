package contract

// contract_test.go —— HTTP/3 契约装配的字段映射与三态语义。
//
// 锁定的不变量：
//  1. 段缺失 → (nil, nil, nil)（引擎跳过构建）；
//  2. addr 为空 → fail-fast（实现的写死默认是 :443，静默抢 443 比错端口更难查）；
//  3. tls 段缺席 → 不传 WithTLSConfig，留给实现自签（QUIC 必须有 TLS，没有「关掉」这一项）；
//  4. 空 tls 段 → 交给 tlsconf 报错（「启用但没证书」不静默回落自签）；
//  5. afterBuild 在 NewServer 之后被调用；其 error 短路装配；
//  6. 业务 Option 追加在契约字段之后（可覆盖 addr）。

import (
	"context"
	"errors"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport/http3"
)

func h3ServerCfg(addr string, tlsSec *bootstrapv1.Server_TLS) *bootstrapv1.Server {
	return &bootstrapv1.Server{Http3: &bootstrapv1.Server_Http3{Addr: addr, Tls: tlsSec}}
}

// TestProvider_SectionMissingSkips 段缺失 → nil server。
func TestProvider_SectionMissingSkips(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv != nil {
		t.Fatalf("srv = %v, want nil (http3 section absent)", srv)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil on skip, want nil")
	}
}

// TestProvider_EmptyAddrFailsFast 配了段却不给地址必须报错，而不是静默去抢 :443。
func TestProvider_EmptyAddrFailsFast(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), h3ServerCfg("", nil))
	if err == nil {
		t.Fatalf("Provider() error = nil, want fail-fast on empty addr (srv=%v)", srv)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil on error path")
	}
}

// TestProvider_SectionPresentConstructs addr 非空 → 构造出 *http3.Server 且落点即契约地址。
func TestProvider_SectionPresentConstructs(t *testing.T) {
	srv, _, err := Provider()(context.Background(), h3ServerCfg("127.0.0.1:19801", nil))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	got, ok := srv.(*http3.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *http3.Server", srv)
	}
	if ep := got.Endpoint(); ep != "127.0.0.1:19801" {
		t.Fatalf("Endpoint() = %q, want 127.0.0.1:19801", ep)
	}
}

// TestBuildOpts_AbsentTlsNotApplied tls 段缺席 → 只有 addr 一条 Option
// （实现据此自签；契约不替业务决定证书，也不生成「空证书」）。
func TestBuildOpts_AbsentTlsNotApplied(t *testing.T) {
	opts, err := buildOpts(&bootstrapv1.Server_Http3{Addr: "127.0.0.1:19802"})
	if err != nil {
		t.Fatalf("buildOpts: %v", err)
	}
	if len(opts) != 1 {
		t.Fatalf("len(buildOpts) = %d, want 1 (addr only; absent tls stays with the implementation)", len(opts))
	}
}

// TestProvider_EmptyTlsSectionFailsFast 空 tls 段 = 「启用但没证书」，必须报错。
func TestProvider_EmptyTlsSectionFailsFast(t *testing.T) {
	_, _, err := Provider()(context.Background(), h3ServerCfg("127.0.0.1:19803", &bootstrapv1.Server_TLS{}))
	if err == nil {
		t.Fatal("error = nil, want fail-fast on empty tls section (no silent self-signed fallback)")
	}
}

// TestProvider_AfterBuildHookInvoked 构造后回调被调用，且拿到返回的同一 server。
func TestProvider_AfterBuildHookInvoked(t *testing.T) {
	var got *http3.Server
	p := Provider(WithAfterBuild(func(_ context.Context, s *http3.Server) error {
		got = s
		return nil
	}))
	srv, _, err := p(context.Background(), h3ServerCfg("127.0.0.1:19804", nil))
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
	p := Provider(WithAfterBuild(func(context.Context, *http3.Server) error { return sentinel }))
	_, _, err := p(context.Background(), h3ServerCfg("127.0.0.1:19805", nil))
	if !errors.Is(err, sentinel) {
		t.Fatalf("Provider() error = %v, want wrapped %v", err, sentinel)
	}
}

// TestProvider_BusinessOptionOverridesAddr 业务 Option 在契约字段之后应用（末者胜）。
func TestProvider_BusinessOptionOverridesAddr(t *testing.T) {
	p := Provider(WithServerOptions(http3.WithAddress("127.0.0.1:19807")))
	srv, _, err := p(context.Background(), h3ServerCfg("127.0.0.1:19806", nil))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if got := srv.(*http3.Server).Endpoint(); got != "127.0.0.1:19807" {
		t.Fatalf("Endpoint() = %q, want 127.0.0.1:19807（业务 Option 未覆盖契约 addr）", got)
	}
}
