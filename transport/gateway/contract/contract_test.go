package contract

// contract_test.go —— gateway 契约装配的字段解析与 fail-fast。
//
// 锁定的不变量（Wave 3.5）：
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
