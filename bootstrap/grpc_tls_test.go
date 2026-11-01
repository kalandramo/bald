package bootstrap

// 装配层的 gRPC TLS 用例：契约 server.grpc.tls 必须在 GrpcServerProvider 构造的
// server 上生效。
//
// 这条链曾经断在装配层——协议层支持 TLS，但 provider 不读 tls 字段，生成的接线
// 里还硬编码 insecure，于是「契约写了 tls」静默等于没写。

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconf/tlsconf"
	"github.com/kalandramo/bald/bconf/tlsconf/tlstest"
	"github.com/kalandramo/bald/transport"
)

// startProvider 调用 provider、启动产物并等待地址就绪。
//
// 与 server_test.go 的 startAndWait 不同：这里 Start 的错误必须可见——启动失败
// 却返回了一个地址，比启动失败更糟（注册中心会拿到幽灵节点）。
func startProvider(t *testing.T, provider ServerProvider, cfg *bootstrapv1.Server) (transport.Server, func()) {
	t.Helper()
	srv, closer, err := provider(context.Background(), cfg)
	if err != nil || srv == nil {
		t.Fatalf("provider = (%v, %v), want non-nil server", srv, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- srv.Start(ctx) }()

	deadline := time.After(3 * time.Second)
	for srv.Endpoint() == "" || strings.HasSuffix(srv.Endpoint(), ":0") {
		select {
		case err := <-started:
			cancel()
			if err != nil {
				t.Fatalf("Start failed before binding: %v", err)
			}
			t.Fatal("Start returned nil while endpoint is still unresolved")
		case <-deadline:
			cancel()
			t.Fatal("server Endpoint not ready in time")
		case <-time.After(10 * time.Millisecond):
		}
	}
	return srv, func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = srv.Stop(stopCtx)
		cancel()
		<-started
		if closer != nil {
			closer()
		}
	}
}

// TestGrpcServerProvider_ContractTLSTakesEffect 断言经装配层起的服务：TLS 客户端
// 可达、明文客户端不可达。
func TestGrpcServerProvider_ContractTLSTakesEffect(t *testing.T) {
	c, err := tlstest.Generate()
	if err != nil {
		t.Fatalf("tlstest.Generate: %v", err)
	}
	provider := GrpcServerProvider(WithGRPCRegister(nil))
	cfg := &bootstrapv1.Server{Grpc: &bootstrapv1.Server_Grpc{
		Addr: "127.0.0.1:0",
		Tls:  c.ServerTLS(),
	}}

	srv, stop := startProvider(t, provider, cfg)
	defer stop()
	ep := srv.Endpoint()

	tlsCfg, err := tlsconf.Client(c.ClientTLS())
	if err != nil {
		t.Fatalf("client tls: %v", err)
	}
	if err := healthRPC(t, ep, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))); err != nil {
		t.Fatalf("契约声明了 tls，TLS 客户端却连不上: %v", err)
	}

	if err := healthRPC(t, ep, grpc.WithTransportCredentials(insecure.NewCredentials())); err == nil {
		t.Error("装配层忽略了契约 tls：明文客户端连上了 TLS 端口")
	}
}

// TestGrpcServerProvider_EmptyTLSContractFailsFast 契约写了 tls 段却配不出证书：
// provider 产物启动即报错，且不报告地址（不得静默退回明文）。
func TestGrpcServerProvider_EmptyTLSContractFailsFast(t *testing.T) {
	cfg := &bootstrapv1.Server{Grpc: &bootstrapv1.Server_Grpc{
		Addr: "127.0.0.1:0",
		Tls:  &bootstrapv1.Server_TLS{},
	}}
	srv, closer, err := GrpcServerProvider()(context.Background(), cfg)
	if err != nil || srv == nil {
		t.Fatalf("provider = (%v, %v), want server", srv, err)
	}
	if closer != nil {
		defer closer()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err == nil {
		t.Fatal("空 tls 段应启动失败")
	}
	if ep := srv.Endpoint(); ep != "" {
		t.Errorf("启动失败后 Endpoint = %q, want 空", ep)
	}
}

// healthRPC 用给定凭据拨号并调用标准健康服务（transport/grpc 默认已注册）。
func healthRPC(t *testing.T, ep string, creds grpc.DialOption) error {
	t.Helper()
	conn, err := grpc.NewClient(strings.TrimPrefix(ep, "grpc://"), creds)
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	return err
}
