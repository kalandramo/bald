package bootstrap

import (
	"context"
	"errors"
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
	httpserver "github.com/kalandramo/bald/transport/http"
)

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
		infoer, ok := srv.(interface {
			GetServiceInfo() map[string]grpc.ServiceInfo
		})
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

// TestHttpServerProvider_IgnoresGatewayDriver 钉住本包的边界：driver 归转码面
// Provider 消费，HttpServerProvider 只按 handler 构 Plain HTTP 面。
func TestHttpServerProvider_IgnoresGatewayDriver(t *testing.T) {
	// server.http.driver 写什么都不改变产物（转码拓扑见 transport/gateway/contract）。
	cfg := &bootstrapv1.Server{
		Http: &bootstrapv1.Server_Http{Addr: ":0", Driver: "grpc-gateway"},
		Grpc: &bootstrapv1.Server_Grpc{Addr: "127.0.0.1:0"},
	}
	srv, closer, err := HttpServerProvider(WithHTTPHandler(http.NewServeMux()))(context.Background(), cfg)
	if err != nil || srv == nil {
		t.Fatalf("provider = (%v, %v), want plain HTTP server", srv, err)
	}
	if closer != nil {
		defer closer()
	}
	if _, ok := srv.(*httpserver.HTTPServer); !ok {
		t.Fatalf("server type = %T, want *httpserver.HTTPServer (driver is not consumed here)", srv)
	}
}
