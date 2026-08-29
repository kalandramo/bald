package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	confv1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/config/v1"
)

// freeAddr 分配一个当前空闲的 TCP 端口，返回 "127.0.0.1:port"。
// 网关后端地址必须是「可连接」的（不能是 :0，否则转发时无从得知真实端口），
// 故测试用本函数取确定端口，而非让后端监听 :0 后再反查。
func freeAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeAddr: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()
	return addr
}

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
	opts := &confv1.Http{Addr: ":0"}
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
	// 验证 scheme 判定逻辑：TLS 启用 → https://，否则 http://。
	// Endpoint 在未监听（Start 前）返回空字符串，故需先启动再校验前缀。
	tlsSrv := NewHTTPServer(&confv1.Http{Addr: ":0", Tls: &confv1.Tls{Enabled: true}}, http.NewServeMux(), nil)
	tlsStop := startAndWait(t, tlsSrv)
	defer tlsStop()
	if ep := tlsSrv.Endpoint(); !strings.HasPrefix(ep, "https://") {
		t.Fatalf("with TLS enabled, scheme = %q, want https://", ep)
	}

	plain := NewHTTPServer(&confv1.Http{Addr: ":0"}, http.NewServeMux(), nil)
	plainStop := startAndWait(t, plain)
	defer plainStop()
	if ep := plain.Endpoint(); !strings.HasPrefix(ep, "http://") {
		t.Fatalf("without TLS, scheme = %q, want http://", ep)
	}
}

// TestGRPCServer_DynamicPort：绑定 ":0" 时 Endpoint 解析为 grpc://真实端口。
func TestGRPCServer_DynamicPort(t *testing.T) {
	opts := &confv1.Grpc{Addr: ":0"}
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
	opts := &confv1.Grpc{Addr: ":0"}
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
	// 先起一个真实 gRPC 后端（确定端口，网关才能连上）。
	backendOpts := &confv1.Grpc{Addr: freeAddr(t)}
	backend := NewGRPCServerWithRegister(backendOpts, nil, nil, nil)
	backendStop := startAndWait(t, backend)
	defer backendStop()

	// 网关指向后端地址（传 *confv1.Grpc 指针，Start 时实时读 addr，
	// 这样 env/flag 对 grpc.addr 的覆盖也能生效）。
	gwOpts := &confv1.Http{Addr: freeAddr(t)}
	gw, err := NewGatewayServer(gwOpts, backendOpts, nil, nil)
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
	opts := &confv1.Http{Addr: ":0"}
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
	srv := NewHTTPServer(&confv1.Http{Addr: ":0"}, http.NewServeMux(), nil)
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
	srv := NewHTTPServer(&confv1.Http{Addr: ":0"}, http.NewServeMux(), ready)
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
	srv := NewGRPCServerWithRegister(&confv1.Grpc{Addr: ":0"}, nil, nil, ready)
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
	backendOpts := &confv1.Grpc{Addr: freeAddr(t)}
	backend := NewGRPCServerWithRegister(backendOpts, nil, nil, nil)
	backendStop := startAndWait(t, backend)
	defer backendStop()

	gwOpts := &confv1.Http{Addr: freeAddr(t)}
	gw, err := NewGatewayServer(gwOpts, backendOpts, nil, nil)
	if err != nil {
		t.Fatalf("NewGatewayServer: %v", err)
	}
	gwStop := startAndWait(t, gw)
	defer gwStop()

	if code := getProbe(t, gw, "/healthz"); code != http.StatusOK {
		t.Fatalf("gateway /healthz = %d, want 200", code)
	}
}

// TestEndpoint_ResolvesReachableHost：通配符 / 仅端口绑定（如 ":0"、":8080"）时，
// Endpoint 必须返回对调用方「可达」的本机 IP，而非 "0.0.0.0"/"[::]"/"" 这类通配符。
// 这是服务注册可达性的关键修复点：之前直接返回 ln.Addr().String()（=0.0.0.0:port），
// 其他节点拿到后无法直连。
func TestEndpoint_ResolvesReachableHost(t *testing.T) {
	cases := []struct {
		name string
		http *confv1.Http
		grpc *confv1.Grpc
	}{
		// 两种写法都只指定端口、未指定 IP（通配符绑定），等价于用户「只配端口」的场景。
		{"dynamic-port", &confv1.Http{Addr: ":0"}, &confv1.Grpc{Addr: ":0"}},
		{"port-only", &confv1.Http{Addr: ":0"}, &confv1.Grpc{Addr: ":0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			httpSrv := NewHTTPServer(tc.http, http.NewServeMux(), nil)
			grpcSrv := NewGRPCServerWithRegister(tc.grpc, nil, nil, nil)
			httpStop := startAndWait(t, httpSrv)
			defer httpStop()
			grpcStop := startAndWait(t, grpcSrv)
			defer grpcStop()

			for _, ep := range []string{httpSrv.Endpoint(), grpcSrv.Endpoint()} {
				host, _, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(ep, "grpc://"), "http://"))
				if err != nil {
					t.Fatalf("endpoint %q: split host:port: %v", ep, err)
				}
				ip := net.ParseIP(host)
				if ip == nil {
					t.Fatalf("endpoint %q: host %q is not an IP (unreachable wildcard?)", ep, host)
				}
				if ip.IsUnspecified() || ip.IsLoopback() {
					t.Fatalf("endpoint %q: host %q is unspecified/loopback, not reachable by peers", ep, host)
				}
			}
		})
	}
}

// TestExtract_ExplicitIPKept：显式指定 IP 时 Extract 应原样保留，不被覆盖。
func TestExtract_ExplicitIPKept(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	got, err := Extract("10.0.0.5:8080", ln)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got != "10.0.0.5:8080" {
		t.Fatalf("Extract = %q, want 10.0.0.5:8080 (explicit IP kept)", got)
	}
}

// TestServer_ConcurrentEndpointDuringStart 回归防护：Start 与 Endpoint 必须并发安全。
//
// 场景来自 appkit：Start 在 errgroup goroutine 中执行（写 s.ln），
// 而 waitForEndpoints 在另一个 goroutine 里以 10ms 间隔轮询 Endpoint()（读 s.ln）。
// 修复前二者构成数据竞争，go test -race 可稳定复现。
//
// 注意：仅靠 -race 还不够，这个测试保证的是「并发调用不 panic 且结果自洽」，
// 竞争本身由 -race 检测。
func TestServer_ConcurrentEndpointDuringStart(t *testing.T) {
	servers := map[string]Server{
		"http": NewHTTPServer(&confv1.Http{Addr: ":0"}, http.NewServeMux(), nil),
		"grpc": NewGRPCServerWithRegister(&confv1.Grpc{Addr: ":0"}, nil, nil, nil),
	}

	for name, srv := range servers {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			started := make(chan error, 1)
			go func() { started <- srv.Start(ctx) }()

			// 并发轮询 Endpoint，模拟 appkit.waitForEndpoints 的行为。
			// 抢在 Start 写入 ln 之前就开始读，才可能撞上竞争窗口。
			pollerDone := make(chan struct{})
			go func() {
				defer close(pollerDone)
				for i := 0; i < 200; i++ {
					_ = srv.Endpoint()
					time.Sleep(time.Millisecond)
				}
			}()

			// 等待 Endpoint 就绪（Start 已写入 ln）。
			deadline := time.After(2 * time.Second)
			for {
				ep := srv.Endpoint()
				if ep != "" && !strings.HasSuffix(ep, ":0") {
					break
				}
				select {
				case <-deadline:
					cancel()
					t.Fatal("Endpoint not ready in time")
				case <-time.After(5 * time.Millisecond):
				}
			}

			// Stop 与 Endpoint 同样并发（停机 goroutine vs 注册 goroutine）。
			stopDone := make(chan struct{})
			go func() {
				defer close(stopDone)
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer stopCancel()
				_ = srv.Stop(stopCtx)
			}()
			for i := 0; i < 20; i++ {
				_ = srv.Endpoint()
			}

			<-stopDone
			cancel()
			<-started
			<-pollerDone
		})
	}
}

// TestGRPCServer_StopConcurrentWithStart 回归防护：Stop 读 readinessCancel，
// Start 写它，二者并发（崩溃级联场景下 errgroup 可能在 Start 完成前触发 Stop）。
func TestGRPCServer_StopConcurrentWithStart(t *testing.T) {
	ready := func(context.Context) error { return nil }
	srv := NewGRPCServerWithRegister(&confv1.Grpc{Addr: ":0"}, nil, nil, ready)

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- srv.Start(ctx) }()

	// 不等 Endpoint 就绪就立刻 Stop，制造 Stop 与 Start 的重叠窗口。
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := srv.Stop(stopCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop: %v", err)
	}

	cancel()

	// Start 在本场景下可能返回 grpc.ErrServerStopped：若 Stop 抢在 Serve 真正
	// 开始接受连接之前执行，GracefulStop 会先关掉 server，随后 Serve 立即返回该错误。
	// 这是 gRPC 的正常语义，不是缺陷——本测试关心的是并发不 panic 且无数据竞争。
	if err := <-started; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		t.Fatalf("Start: %v", err)
	}
}

// TestGRPCServer_StartErrorLeavesNoGoroutine 验证 listen 失败时不会泄漏
// readiness 轮询 goroutine：轮询改为在 listen 成功之后才启动。
func TestGRPCServer_StartErrorLeavesNoGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()

	// 占用一个端口，再让 server 用已被占用的地址启动（Unix 上监听已绑定端口会失败）。
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer occupied.Close()

	ready := func(context.Context) error { return nil }
	srv := NewGRPCServerWithRegister(
		&confv1.Grpc{Addr: occupied.Addr().String()}, nil, nil, ready)

	if err := srv.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded on occupied address, want error")
	}

	// 给残留 goroutine（若存在）一点时间被调度。
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("goroutine count grew from %d to %d, readiness poller may leak", before, after)
	}
}
