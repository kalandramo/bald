package contract

// contract_test.go —— gateway 契约装配的字段解析与 fail-fast。
//
// 锁定的不变量：
//  1. 段缺失 → (nil, nil, nil)；
//  2. register 为 nil → fail-fast（配置声明了转码面却无内容）；
//  3. addr 为空 → fail-fast；
//  4. backend_grpc_addr 为空时回退 server.grpc.addr；
//  5. 两者都空 → fail-fast。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/transport/gateway"
)

func okRegister(context.Context, *grpc.ClientConn) (http.Handler, error) {
	return http.NewServeMux(), nil
}

// TestProvider_SectionMissingSkips 段缺失 → nil server。
func TestProvider_SectionMissingSkips(t *testing.T) {
	p := Provider(okRegister)
	srv, cleanup, err := p(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv != nil || cleanup != nil {
		t.Fatalf("srv=%v cleanup!=nil=%v, want both nil (gateway section absent)", srv, cleanup != nil)
	}
}

// TestProvider_NilRegisterFailsFast register 必供。
func TestProvider_NilRegisterFailsFast(t *testing.T) {
	p := Provider(nil)
	cfg := &bootstrapv1.Server{Gateway: &bootstrapv1.Server_Gateway{Addr: ":8081"}}
	_, _, err := p(context.Background(), cfg)
	if err == nil {
		t.Fatal("Provider() = nil error, want fail-fast (register func required)")
	}
	if !strings.Contains(err.Error(), "register") {
		t.Fatalf("error %q should mention register func", err)
	}
}

// TestProvider_EmptyAddrFailsFast addr 必填。
func TestProvider_EmptyAddrFailsFast(t *testing.T) {
	p := Provider(okRegister)
	cfg := &bootstrapv1.Server{Gateway: &bootstrapv1.Server_Gateway{}}
	_, _, err := p(context.Background(), cfg)
	if err == nil {
		t.Fatal("Provider() = nil error, want fail-fast (addr required)")
	}
}

// TestProvider_BackendAddrFallsBackToGrpc backend_grpc_addr 为空 → 用 server.grpc.addr。
func TestProvider_BackendAddrFallsBackToGrpc(t *testing.T) {
	p := Provider(okRegister)
	cfg := &bootstrapv1.Server{
		Grpc:    &bootstrapv1.Server_Grpc{Addr: ":9090"},
		Gateway: &bootstrapv1.Server_Gateway{Addr: ":8081"},
	}
	srv, _, err := p(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil (fallback to grpc addr)", err)
	}
	if _, ok := srv.(*gateway.GatewayServer); !ok {
		t.Fatalf("srv type = %T, want *gateway.GatewayServer", srv)
	}
}

// TestProvider_NoBackendAddrFailsFast 两者都空 → fail-fast。
func TestProvider_NoBackendAddrFailsFast(t *testing.T) {
	p := Provider(okRegister)
	cfg := &bootstrapv1.Server{Gateway: &bootstrapv1.Server_Gateway{Addr: ":8081"}}
	_, _, err := p(context.Background(), cfg)
	if err == nil {
		t.Fatal("Provider() = nil error, want fail-fast (no backend addr)")
	}
}

// TestProvider_ExplicitBackendAddrUsed 显式 backend_grpc_addr 优先。
func TestProvider_ExplicitBackendAddrUsed(t *testing.T) {
	p := Provider(okRegister)
	cfg := &bootstrapv1.Server{
		Grpc:    &bootstrapv1.Server_Grpc{Addr: ":9090"},
		Gateway: &bootstrapv1.Server_Gateway{Addr: ":8081", BackendGrpcAddr: ":9999"},
	}
	srv, _, err := p(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider() error = %v", err)
	}
	if srv == nil {
		t.Fatal("srv = nil, want *gateway.GatewayServer")
	}
}

// opts 必须透传到 gateway.NewGatewayServer：契约层不解释地址语义，注入的
// Discovery（或任何 Option）只由 gateway 自己消费。两条构造路径都要覆盖。
func TestProviderThreadsGatewayOptions(t *testing.T) {
	var applied int
	opt := func(*gateway.GatewayServer) { applied++ }

	factory := Provider(okRegister, opt)
	cfg := &bootstrapv1.Server{Gateway: &bootstrapv1.Server_Gateway{
		Addr: ":8081", BackendGrpcAddr: "discovery:///backend",
	}}
	if _, _, err := factory(context.Background(), cfg); err != nil {
		t.Fatalf("build: %v", err)
	}
	if applied != 1 {
		t.Fatalf("Option 被调用 %d 次, want 1", applied)
	}
}

func TestHTTPProviderThreadsGatewayOptions(t *testing.T) {
	var applied int
	opt := func(*gateway.GatewayServer) { applied++ }

	p := HTTPProvider(stubRegister, opt)
	if applied != 0 {
		t.Fatalf("构造 Provider 阶段就调用了 Option（%d 次）", applied)
	}
	srv, cleanup, err := p.Build(context.Background(),
		httpScope(httpServerCfg(DriverGRPCGateway), &appkit.Decl{}), appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup != nil {
		_ = cleanup(context.Background())
	}
	if srv == nil {
		t.Fatal("srv = nil")
	}
	if applied != 1 {
		t.Fatalf("Option 被调用 %d 次, want 1", applied)
	}
}
