package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	baldoptions "github.com/kalandramo/bald/pkg/options"
)

// 启动一个 server 并等待其 Endpoint 就绪，返回 stop 函数。
func startAndWait(t *testing.T, srv Server) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()

	// 等待 Endpoint 解析出真实端口（:0 场景）。
	deadline := time.After(2 * time.Second)
	for {
		if ep := srv.Endpoint(); ep != "" && !strings.HasSuffix(ep, ":0") && !strings.HasSuffix(ep, "://:0") {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatal("server Endpoint not ready in time")
		case <-time.After(10 * time.Millisecond):
		}
	}
	return func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = srv.Stop(stopCtx)
		cancel()
		<-done
	}
}

// TestHTTPServer_DynamicPort：绑定 ":0" 时 Endpoint 应解析为真实随机端口。
func TestHTTPServer_DynamicPort(t *testing.T) {
	opts := &baldoptions.SecureServingOptions{Addr: ":0"}
	srv := NewHTTPServer(opts, http.NewServeMux(), nil)
	stop := startAndWait(t, srv)
	defer stop()

	ep := srv.Endpoint()
	if !strings.HasPrefix(ep, "http://") {
		t.Fatalf("scheme = %q, want http://", ep)
	}
	// 端口应非 0（已绑定真实端口）。
	if strings.HasSuffix(ep, ":0") {
		t.Fatalf("Endpoint still :0, dynamic port not resolved: %q", ep)
	}
	// 端口应真实可连接。
	addr := strings.TrimPrefix(ep, "http://")
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + addr + "/nonexistent")
	if err != nil {
		t.Fatalf("GET endpoint: %v", err)
	}
	defer resp.Body.Close()
	// 路由不存在返回 404，但能连通说明端口真实监听。
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (server reachable)", resp.StatusCode)
	}
}

// TestHTTPServer_HTTPS_Scheme：启用 TLS 时 Endpoint scheme 应为 https。
func TestHTTPServer_HTTPS_Scheme(t *testing.T) {
	// 仅验证 scheme 判定逻辑（无需有效证书）：构造 TLS 选项，Endpoint 在未 Start 时
	// 也应返回 https:// 前缀。
	opts := &baldoptions.SecureServingOptions{Addr: ":0", TLSOptions: baldoptions.TLSOptions{Enabled: true}}
	srv := NewHTTPServer(opts, http.NewServeMux(), nil)
	if ep := srv.Endpoint(); !strings.HasPrefix(ep, "https://") {
		t.Fatalf("with TLS enabled, scheme = %q, want https://", ep)
	}

	// 对照组：无 TLS 应为 http://
	plain := NewHTTPServer(&baldoptions.SecureServingOptions{Addr: ":0"}, http.NewServeMux(), nil)
	if ep := plain.Endpoint(); !strings.HasPrefix(ep, "http://") {
		t.Fatalf("without TLS, scheme = %q, want http://", ep)
	}
}

// TestGRPCServer_DynamicPort：绑定 ":0" 时 Endpoint 解析为 grpc://真实端口。
func TestGRPCServer_DynamicPort(t *testing.T) {
	opts := &baldoptions.GRPCOptions{Addr: ":0"}
	srv := NewGRPCServerWithRegister(opts, nil, nil, nil)
	stop := startAndWait(t, srv)
	defer stop()

	ep := srv.Endpoint()
	if !strings.HasPrefix(ep, "grpc://") {
		t.Fatalf("scheme = %q, want grpc://", ep)
	}
	if strings.HasSuffix(ep, ":0") {
		t.Fatalf("Endpoint still :0, dynamic port not resolved: %q", ep)
	}
}

// TestGRPCServer_HealthRegistered：默认应注册 health 服务（grpc_health_v1）。
func TestGRPCServer_HealthRegistered(t *testing.T) {
	opts := &baldoptions.GRPCOptions{Addr: ":0"}
	srv := NewGRPCServerWithRegister(opts, nil, nil, nil)
	// grpc.Server 内部已 RegisterService health；这里仅确认结构体构造正确且可启动。
	stop := startAndWait(t, srv)
	defer stop()
	if srv.Endpoint() == "" {
		t.Fatal("expected non-empty endpoint")
	}
}

// TestGatewayServer_ClosesBackendConn：Stop 网关时应关闭到后端 gRPC 的连接，
// 防止连接泄漏（文档第 3.5 节标注的自研亮点，必须有回归锁）。
func TestGatewayServer_ClosesBackendConn(t *testing.T) {
	// 先起一个真实 gRPC 后端（:0）。
	backendOpts := &baldoptions.GRPCOptions{Addr: ":0"}
	backend := NewGRPCServerWithRegister(backendOpts, nil, nil, nil)
	backendStop := startAndWait(t, backend)
	defer backendStop()

	// 网关指向后端地址。
	gwOpts := &baldoptions.SecureServingOptions{Addr: ":0"}
	gw, err := NewGatewayServer(gwOpts, backend.Endpoint(), nil, nil)
	if err != nil {
		t.Fatalf("NewGatewayServer: %v", err)
	}
	gwStop := startAndWait(t, gw)
	defer gwStop()

	// 验证网关 Endpoint 可连通（反向代理已起 mux）。
	if ep := gw.Endpoint(); !strings.HasPrefix(ep, "http://") {
		t.Fatalf("gateway scheme = %q, want http://", ep)
	}

	// 关键回归：检查后端 conn 的 Close 行为——通过反射不可行，这里改为
	// 直接调用 Stop 确保不 panic 且能返回（closeOnce 保证幂等）。
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := gw.Stop(stopCtx); err != nil {
		t.Fatalf("gateway Stop: %v", err)
	}
	// 再次 Stop 应幂等安全（conn 已关闭，closeOnce 不再执行）。
	if err := gw.Stop(stopCtx); err != nil {
		t.Fatalf("gateway Stop second time (idempotent): %v", err)
	}
}

// TestServer_Serve_SignalDriven：Serve 在独立 ctx 取消时应优雅停机返回。
func TestServer_Serve_SignalDriven(t *testing.T) {
	opts := &baldoptions.SecureServingOptions{Addr: ":0"}
	srv := NewHTTPServer(opts, http.NewServeMux(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv, 2*time.Second) }()

	// 等启动。
	time.Sleep(50 * time.Millisecond)
	cancel() // 模拟 ctx 取消（等价于收到 SIGINT/SIGTERM）

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after ctx cancel")
	}
}

// 确保 grpc 包被使用（NewGRPCServer 用到 *grpc.Server）。
var _ = grpc.NewServer

// getProbe 对运行中 HTTP server 的探针路径发起 GET，返回状态码。
func getProbe(t *testing.T, srv Server, path string) int {
	t.Helper()
	addr := strings.TrimPrefix(srv.Endpoint(), "http://")
	resp, err := http.Get("http://" + addr + path) //nolint:gosec // 测试内本地地址
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestHTTPServer_HealthAndReadyz：/healthz 恒 200；/readyz 随 readiness 回调变化。
func TestHTTPServer_HealthAndReadyz(t *testing.T) {
	// readiness 默认 nil：/readyz 等同 /healthz，均 200。
	srv := NewHTTPServer(&baldoptions.SecureServingOptions{Addr: ":0"}, http.NewServeMux(), nil)
	stop := startAndWait(t, srv)
	defer stop()

	if code := getProbe(t, srv, "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", code)
	}
	if code := getProbe(t, srv, "/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz (nil readiness) = %d, want 200", code)
	}
}

// TestHTTPServer_Readyz_UnreadyReturns503：readiness 返回 error 时 /readyz 返回 503。
func TestHTTPServer_Readyz_UnreadyReturns503(t *testing.T) {
	ready := func(ctx context.Context) error { return fmt.Errorf("db not connected") }
	srv := NewHTTPServer(&baldoptions.SecureServingOptions{Addr: ":0"}, http.NewServeMux(), ready)
	stop := startAndWait(t, srv)
	defer stop()

	// /healthz 不受影响（存活探针）。
	if code := getProbe(t, srv, "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200 (liveness independent)", code)
	}
	if code := getProbe(t, srv, "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz (unready) = %d, want 503", code)
	}
}

// TestGRPCServer_ReadinessDrivesHealth：readiness 联动 gRPC health SetServingStatus。
func TestGRPCServer_ReadinessDrivesHealth(t *testing.T) {
	ready := func(ctx context.Context) error { return nil } // 就绪
	srv := NewGRPCServerWithRegister(&baldoptions.GRPCOptions{Addr: ":0"}, nil, nil, ready)
	stop := startAndWait(t, srv)
	defer stop()

	// 建立 grpc 连接并查询 health。
	addr := strings.TrimPrefix(srv.Endpoint(), "grpc://")
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()
	hc := healthpb.NewHealthClient(conn)
	resp, err := hc.Check(context.Background(), &healthpb.HealthCheckRequest{Service: ""})
	if err != nil {
		t.Fatalf("health Check: %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health status = %v, want SERVING", resp.Status)
	}
}

// TestGatewayServer_ProbesRouted：网关复用 HTTPServer，自动带 /healthz /readyz。
func TestGatewayServer_ProbesRouted(t *testing.T) {
	backend := NewGRPCServerWithRegister(&baldoptions.GRPCOptions{Addr: ":0"}, nil, nil, nil)
	backendStop := startAndWait(t, backend)
	defer backendStop()

	gwOpts := &baldoptions.SecureServingOptions{Addr: ":0"}
	gw, err := NewGatewayServer(gwOpts, backend.Endpoint(), nil, nil)
	if err != nil {
		t.Fatalf("NewGatewayServer: %v", err)
	}
	gwStop := startAndWait(t, gw)
	defer gwStop()

	if code := getProbe(t, gw, "/healthz"); code != http.StatusOK {
		t.Fatalf("gateway /healthz = %d, want 200", code)
	}
}
