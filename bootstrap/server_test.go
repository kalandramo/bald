package bootstrap

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/health"
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

// TestGrpcServerProvider_ReflectionFlag 契约 reflection 由装配层消费：
// false 不注册、true 注册（transport 已不看这个字段）。
func TestGrpcServerProvider_ReflectionFlag(t *testing.T) {
	for _, want := range []bool{false, true} {
		cfg := &bootstrapv1.Server{
			Grpc: &bootstrapv1.Server_Grpc{Addr: ":0", Reflection: want},
		}
		srv, closer, err := GrpcServerProvider(WithGRPCRegister(func(s *grpc.Server) {}))(context.Background(), cfg)
		if err != nil || srv == nil {
			t.Fatalf("provider(reflection=%t) = (%v, %v), want non-nil", want, srv, err)
		}
		if closer != nil {
			closer()
		}
		// 有无 health 包装都满足该接口（grpcHealthServer 内嵌 *grpcserver.GRPCServer）。
		infoer, ok := srv.(interface{ GetServiceInfo() map[string]grpc.ServiceInfo })
		if !ok {
			t.Fatalf("server %T does not expose GetServiceInfo", srv)
		}
		got := false
		for name := range infoer.GetServiceInfo() {
			if strings.Contains(name, "reflection") {
				got = true
			}
		}
		if got != want {
			t.Fatalf("reflection registered = %t, want %t", got, want)
		}
	}
}

// waitEndpoint 等待 server 解析出真实监听地址（:0 动态端口）。
func waitEndpoint(t *testing.T, srv transport.Server) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for srv.Endpoint() == "" || strings.HasSuffix(srv.Endpoint(), ":0") {
		select {
		case <-deadline:
			t.Fatal("endpoint not ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// grpcHealthStatus 查询 gRPC 标准健康服务的整体状态（"" 服务）。
func grpcHealthStatus(t *testing.T, ep string) healthpb.HealthCheckResponse_ServingStatus {
	t.Helper()
	conn, err := grpc.NewClient(strings.TrimPrefix(ep, "grpc://"),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	defer conn.Close()
	resp, err := healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	return resp.GetStatus()
}

// TestGrpcServerProvider_HealthSync 装配层推送：health.Health 的聚合结果经后台
// 轮询同步到 gRPC 标准健康服务（NOT_SERVING ⇄ SERVING）。
func TestGrpcServerProvider_HealthSync(t *testing.T) {
	var ready atomic.Bool
	h := health.New()
	h.Register("dep", health.PingFunc(func(context.Context) error {
		if !ready.Load() {
			return errors.New("dep not ready")
		}
		return nil
	}))

	cfg := &bootstrapv1.Server{Grpc: &bootstrapv1.Server_Grpc{Addr: ":0"}}
	srv, closer, err := GrpcServerProvider(WithGRPCHealth(h, 10*time.Millisecond))(context.Background(), cfg)
	if err != nil || srv == nil {
		t.Fatalf("provider = (%v, %v), want non-nil", srv, err)
	}
	if closer != nil {
		defer closer()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Stop(stopCtx)
		stopCancel()
		cancel()
		<-done
	}()

	waitEndpoint(t, srv)
	if got := grpcHealthStatus(t, srv.Endpoint()); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status = %v, want NOT_SERVING (dependency down)", got)
	}

	ready.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for grpcHealthStatus(t, srv.Endpoint()) != healthpb.HealthCheckResponse_SERVING {
		if time.Now().After(deadline) {
			t.Fatal("status should recover to SERVING")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestHttpServerProvider_PlainHandler 纯 HTTP 模式：注入 handler 即可启动；
// 协议实现不注册任何框架路由（探针归装配层/业务）。
func TestHttpServerProvider_PlainHandler(t *testing.T) {
	cfg := &bootstrapv1.Server{
		Http: &bootstrapv1.Server_Http{Addr: ":0"},
	}
	srv, closer, err := HttpServerProvider(WithHTTPHandler(http.NewServeMux()))(context.Background(), cfg)
	if err != nil || srv == nil {
		t.Fatalf("provider = (%v, %v), want non-nil server", srv, err)
	}
	if closer != nil {
		defer closer()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	defer func() {
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
	}()

	waitEndpoint(t, srv)
	client := &http.Client{Timeout: time.Second}
	addr := "http://" + strings.TrimPrefix(srv.Endpoint(), "http://")
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := client.Get(addr + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 (no framework routes in transport)", path, resp.StatusCode)
		}
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
