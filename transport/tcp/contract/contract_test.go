package contract

// contract_test.go —— server.tcp 段 → tcp.Option 映射与三态语义（构造期判据）。
//
// 锁定的不变量：
//  1. 段缺失 → (nil, nil, nil)（引擎跳过）；
//  2. 段存在但 addr 为空 → fail-fast（空 addr 会静默落到 ":0" 随机端口）；
//  3. 段存在且 addr 非空 → 产物 *tcp.Server，且地址确实进了构造参数；
//  4. 空 tls 段（`tls: {}`）→ 报错而非回落明文；
//  5. afterBuild 回调拿到的是返回的那台 server；回调报错 → 装配短路；
//  6. WithServerOptions 追加在契约字段之后 → 业务可覆盖契约值。
//
// 「映射真的落到线上」（端口在听、TLS 真握手）由 mapping_offline_test.go 判。

import (
	"context"
	"errors"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport/tcp"
)

func tcpServerCfg(addr string, tlsSec *bootstrapv1.Server_TLS) *bootstrapv1.Server {
	return &bootstrapv1.Server{Tcp: &bootstrapv1.Server_Tcp{Addr: addr, Tls: tlsSec}}
}

// TestProvider_SectionMissingSkips 段缺失 → nil server（引擎跳过构建）。
func TestProvider_SectionMissingSkips(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv != nil || cleanup != nil {
		t.Fatalf("(srv, cleanup) = (%v, %v), want (nil, nil)", srv, cleanup == nil)
	}
}

// TestProvider_EmptyAddrFailsFast 配了段却不给监听地址 = 漏配，必须显式报错。
func TestProvider_EmptyAddrFailsFast(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), tcpServerCfg("", nil))
	if err == nil {
		t.Fatalf("Provider() error = nil, want fail-fast on empty addr (srv=%v)", srv)
	}
	if !strings.Contains(err.Error(), "server.tcp.addr is required") {
		t.Fatalf("err = %v, want addr required", err)
	}
	if srv != nil || cleanup != nil {
		t.Fatalf("(srv, cleanup) = (%v, isNil=%v), want (nil, nil) on error", srv, cleanup == nil)
	}
}

// TestProvider_SectionPresentConstructs addr 进了构造参数（Endpoint 读回同源字段）。
func TestProvider_SectionPresentConstructs(t *testing.T) {
	srv, _, err := Provider()(context.Background(), tcpServerCfg("127.0.0.1:19601", nil))
	if err != nil {
		t.Fatalf("Provider() error = %v", err)
	}
	ts, ok := srv.(*tcp.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *tcp.Server", srv)
	}
	if got := ts.Endpoint(); got != "tcp://127.0.0.1:19601" {
		t.Fatalf("Endpoint() = %q, want tcp://127.0.0.1:19601", got)
	}
}

// TestProvider_EmptyTlsSectionFailsFast `tls: {}` 是「启用但没证书」，
// 回落明文等于把漏配伪装成一次显式选择。
func TestProvider_EmptyTlsSectionFailsFast(t *testing.T) {
	srv, _, err := Provider()(context.Background(), tcpServerCfg("127.0.0.1:19602", &bootstrapv1.Server_TLS{}))
	if err == nil {
		t.Fatalf("error = nil, want tls fail-fast (srv=%v)", srv)
	}
	if !strings.Contains(err.Error(), "server.tcp.tls") {
		t.Fatalf("err = %v, want attribution to server.tcp.tls", err)
	}
}

// TestProvider_AfterBuildHookInvoked 回调在构造后执行，且拿到返回的那台 server。
func TestProvider_AfterBuildHookInvoked(t *testing.T) {
	var got *tcp.Server
	p := Provider(WithAfterBuild(func(_ context.Context, s *tcp.Server) error {
		got = s
		return nil
	}))
	srv, _, err := p(context.Background(), tcpServerCfg("127.0.0.1:19603", nil))
	if err != nil {
		t.Fatalf("Provider() error = %v", err)
	}
	if got == nil {
		t.Fatal("afterBuild hook not invoked")
	}
	if any(got) != any(srv) {
		t.Fatal("hook received a different server than the one returned")
	}
}

// TestProvider_AfterBuildErrorPropagates 回调报错 → 装配短路并带出处。
func TestProvider_AfterBuildErrorPropagates(t *testing.T) {
	sentinel := errors.New("wiring failed")
	p := Provider(WithAfterBuild(func(context.Context, *tcp.Server) error { return sentinel }))
	_, _, err := p(context.Background(), tcpServerCfg("127.0.0.1:19604", nil))
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapped %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "after-build hook") {
		t.Fatalf("err = %v, want attribution to after-build hook", err)
	}
}

// TestProvider_BusinessOptionOverridesAddr 业务 Option 在契约字段之后应用。
func TestProvider_BusinessOptionOverridesAddr(t *testing.T) {
	p := Provider(WithServerOptions(tcp.WithAddress("127.0.0.1:19606")))
	srv, _, err := p(context.Background(), tcpServerCfg("127.0.0.1:19605", nil))
	if err != nil {
		t.Fatalf("Provider() error = %v", err)
	}
	if got := srv.(*tcp.Server).Endpoint(); got != "tcp://127.0.0.1:19606" {
		t.Fatalf("Endpoint() = %q, want the business option address 19606 (override order)", got)
	}
}
