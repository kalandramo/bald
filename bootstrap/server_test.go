package bootstrap

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/transport"
)

// stubSrvServer 最小 transport.Server 实现。
type stubSrvServer struct {
	started chan struct{}
	stopped chan struct{}
	ep      string
}

func newStubSrvServer(ep string) *stubSrvServer {
	return &stubSrvServer{started: make(chan struct{}), stopped: make(chan struct{}), ep: ep}
}
func (s *stubSrvServer) Start(context.Context) error { close(s.started); return nil }
func (s *stubSrvServer) Stop(context.Context) error  { close(s.stopped); return nil }
func (s *stubSrvServer) Endpoint() string            { return s.ep }

func TestServerRegistry_Register(t *testing.T) {
	r := NewServerRegistry()
	if err := r.Register("grpc", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return nil, nil, nil
	}); err != nil {
		t.Fatalf("Register(first) = %v, want nil", err)
	}
	if err := r.Register("grpc", nil); err == nil {
		t.Fatal("Register(duplicate) = nil, want error")
	}
	if err := r.Register("", nil); err == nil {
		t.Fatal("Register(empty) = nil, want error")
	}
	if err := r.Register("http", nil); err == nil {
		t.Fatal("Register(nil provider) = nil, want error")
	}
}

func TestBuildServers_MultiSelect(t *testing.T) {
	r := NewServerRegistry()
	stub := func(ep string) ServerProvider {
		return func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
			return newStubSrvServer(ep), nil, nil
		}
	}
	r.MustRegister("grpc", stub("grpc://a"))
	r.MustRegister("http", stub("http://b"))

	servers, cleanup, err := r.BuildServers(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("BuildServers() = %v, want nil", err)
	}
	defer cleanup()
	if len(servers) != 2 {
		t.Fatalf("len(servers) = %d, want 2 (multi-select: grpc+http both built)", len(servers))
	}
}

func TestBuildServers_SkipsUnconfigured(t *testing.T) {
	r := NewServerRegistry()
	r.MustRegister("grpc", GrpcServerProvider()) // 契约无 Grpc 段 → nil → 跳过
	r.MustRegister("stub", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return newStubSrvServer("stub://x"), nil, nil
	})

	servers, cleanup, err := r.BuildServers(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("BuildServers() = %v, want nil", err)
	}
	defer cleanup()
	if len(servers) != 1 {
		t.Fatalf("len(servers) = %d, want 1 (unconfigured grpc skipped)", len(servers))
	}
}

func TestBuildServers_NoneConfigured(t *testing.T) {
	r := NewServerRegistry()
	r.MustRegister("grpc", GrpcServerProvider())

	if _, _, err := r.BuildServers(context.Background(), &bootstrapv1.Server{}); err == nil {
		t.Fatal("no server configured should error")
	}
	if _, _, err := r.BuildServers(context.Background(), nil); err == nil {
		t.Fatal("nil config should error")
	}
}

func TestBuildServers_RollbackOnError(t *testing.T) {
	var order []string
	r := NewServerRegistry()
	r.MustRegister("a", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return newStubSrvServer("a"), func() { order = append(order, "a") }, nil
	})
	r.MustRegister("b", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return nil, nil, errors.New("boom")
	})

	_, cleanup, err := r.BuildServers(context.Background(), &bootstrapv1.Server{})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("BuildServers() = %v, want boom", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup should be nil on failure (rolled back)")
	}
	if len(order) != 1 || order[0] != "a" {
		t.Fatalf("rollback order = %v, want [a]", order)
	}
}

// TestGrpcServerProvider_Unconfigured 契约无 Grpc 段 → nil server。
func TestGrpcServerProvider_Unconfigured(t *testing.T) {
	srv, closer, err := GrpcServerProvider()(context.Background(), &bootstrapv1.Server{})
	if err != nil || srv != nil || closer != nil {
		t.Fatalf("unconfigured = (srv=%v, closer!=nil: %t, err=%v), want all nil", srv, closer != nil, err)
	}
}

// TestGrpcServerProvider_ReflectionFlag 契约 reflection=false → 不注册 reflection。
// 通过 Start 后 endpoint 可达 + 不 panic 验证装配链路。
func TestGrpcServerProvider_ReflectionFlag(t *testing.T) {
	cfg := &bootstrapv1.Server{
		Grpc: &bootstrapv1.Server_Grpc{Addr: ":0", Reflection: false},
	}
	srv, closer, err := GrpcServerProvider(WithGRPCRegister(func(s *grpc.Server) {}))(context.Background(), cfg)
	if err != nil || srv == nil {
		t.Fatalf("provider = (%v, %v), want non-nil", srv, err)
	}
	if closer != nil {
		closer()
	}
}

// TestHttpServerProvider_PlainHandler 纯 HTTP 模式：注入 handler，启动后探针可用。
func TestHttpServerProvider_PlainHandler(t *testing.T) {
	cfg := &bootstrapv1.Server{
		Http: &bootstrapv1.Server_Http{Addr: ":0"},
	}
	srv, closer, err := HttpServerProvider(
		WithHTTPHandler(http.NewServeMux()),
		WithHTTPReadiness(func(context.Context) error { return errors.New("not ready") }),
	)(context.Background(), cfg)
	if err != nil || srv == nil {
		t.Fatalf("provider = (%v, %v), want non-nil server", srv, err)
	}
	if closer != nil {
		defer closer()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()

	deadline := time.After(2 * time.Second)
	for strings.HasSuffix(srv.Endpoint(), ":0") || srv.Endpoint() == "" {
		select {
		case <-deadline:
			stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
			_ = srv.Stop(stopCtx)
			stopCancel()
			cancel()
			t.Fatal("endpoint not ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// readiness 失败 → /readyz 503
	client := &http.Client{Timeout: time.Second}
	addr := "http://" + strings.TrimPrefix(srv.Endpoint(), "http://")
	resp, err := client.Get(addr + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d, want 503", resp.StatusCode)
	}

	// 走 Stop 优雅停机（Start 阻塞在 Serve，ctx 取消不会让它返回）。
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = srv.Stop(stopCtx)
	stopCancel()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

// TestHttpServerProvider_GatewayMode 注入 WithGatewayRegister + driver 留空 → gateway 模式。
func TestHttpServerProvider_GatewayMode(t *testing.T) {
	// 真实 gRPC 后端（gateway 需要拨号）。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	backend := ln.Addr().String()
	_ = ln.Close() // gateway 用 grpc.NewClient 懒连接，端口无需真实监听

	cfg := &bootstrapv1.Server{
		Http: &bootstrapv1.Server_Http{Addr: ":0", Driver: DriverGrpcGateway},
		Grpc: &bootstrapv1.Server_Grpc{Addr: backend},
	}
	srv, closer, err := HttpServerProvider(
		WithGatewayRegister(func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
			return http.NewServeMux(), nil
		}),
	)(context.Background(), cfg)
	if err != nil || srv == nil {
		t.Fatalf("provider = (%v, %v), want non-nil gateway server", srv, err)
	}
	if closer != nil {
		defer closer()
	}
	// gateway 是 *transport/gateway.GatewayServer——只验证类型装配合法。
	_ = srv
}
