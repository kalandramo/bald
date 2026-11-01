package contract

// provider_test.go —— 段驱动导出（AppProvider / HTTPProvider）的单元测试：
//   1. 段路径声明正确、Option 嵌入面可用（Apply 追加进声明且**携带工厂**）；
//   2. 段在 → Build 成功：产物 transport.Server 进服务器列表；
//   3. 子段缺失 → (nil, nil, nil) 跳过语义；register nil / addr 必填 fail-fast；
//   4. 顶层段缺失 → bug 级错误；
//   5. HTTPProvider（接管 server.http 的同端口转码面）：节点与 std 同 Key、
//      driver 准入判据、探针外层包装（起真实面服务器打 HTTP 请求验活）。
//
// NewGatewayServer 仅构造（连接与监听延迟到 Start），离线可实例化；验活用例
// 用 127.0.0.1:0 真实监听，拨号上游是懒连接故无需真实 gRPC 后端。

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/health"

	"github.com/kalandramo/bald/pkg/appkit"
	gateway "github.com/kalandramo/bald/transport/gateway"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider(nil).Segments()
	if len(segs) != 1 || segs[0] != "server.gateway" {
		t.Fatalf("Segments() = %v, want [server.gateway]", segs)
	}
}

// Apply 落声明的是携带工厂的接收者本身——零值副本会让 Build 直呼 nil 工厂。
func TestAppProvider_ApplyPreservesFactory(t *testing.T) {
	p := AppProvider(func(context.Context, *grpc.ClientConn) (http.Handler, error) {
		return http.NewServeMux(), nil
	})
	d := &appkit.Decl{}
	p.Apply(d)
	if len(d.Providers) != 1 {
		t.Fatalf("decl providers = %d, want 1", len(d.Providers))
	}
	srv, _, err := d.Providers[0].Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{
			Server: &bootstrapv1.Server{
				Gateway: &bootstrapv1.Server_Gateway{
					Addr:            "127.0.0.1:18081",
					BackendGrpcAddr: "127.0.0.1:19090",
				},
			},
		},
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build via decl: %v", err)
	}
	if srv == nil {
		t.Fatal("srv = nil via decl provider, factory was lost in Apply")
	}
}

func TestAppProvider_BuildConstructsServer(t *testing.T) {
	srv, cleanup, err := AppProvider(func(context.Context, *grpc.ClientConn) (http.Handler, error) {
		return http.NewServeMux(), nil
	}).Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{
			Server: &bootstrapv1.Server{
				Gateway: &bootstrapv1.Server_Gateway{
					Addr:            "127.0.0.1:18081",
					BackendGrpcAddr: "127.0.0.1:19090",
				},
			},
		},
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil (gateway conn lifecycle belongs to Start/Stop)")
	}
	if srv == nil {
		t.Fatal("srv = nil, want *gateway.GatewayServer")
	}
}

func TestAppProvider_RegisterNilFailsFast(t *testing.T) {
	_, _, err := AppProvider(nil).Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{
			Server: &bootstrapv1.Server{
				Gateway: &bootstrapv1.Server_Gateway{Addr: "127.0.0.1:18081"},
			},
		},
	}, appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "no register func provided") {
		t.Fatalf("err = %v, want register func required", err)
	}
}

func TestAppProvider_AddrRequiredFailsFast(t *testing.T) {
	_, _, err := AppProvider(func(context.Context, *grpc.ClientConn) (http.Handler, error) {
		return http.NewServeMux(), nil
	}).Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{
			Server: &bootstrapv1.Server{
				Gateway: &bootstrapv1.Server_Gateway{BackendGrpcAddr: "127.0.0.1:19090"},
			},
		},
	}, appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "server.gateway.addr is required") {
		t.Fatalf("err = %v, want addr required", err)
	}
}

func TestAppProvider_SubSectionMissingSkips(t *testing.T) {
	srv, cleanup, err := AppProvider(nil).Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{Server: &bootstrapv1.Server{}},
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build error = %v, want nil (skip)", err)
	}
	if srv != nil || cleanup != nil {
		t.Fatalf("(srv, cleanup) = (%v, %v), want (nil, nil) on skip", srv, cleanup)
	}
}

func TestAppProvider_TopSectionMissingIsBug(t *testing.T) {
	_, _, err := AppProvider(nil).Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{},
	}, appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "report this as a bug") {
		t.Fatalf("err = %v, want bug report (engine gates by exact path)", err)
	}
}

// ---------------------------------------------------------------------------
// HTTPProvider：接管 server.http 段的同端口转码面
// ---------------------------------------------------------------------------

const gwRegisterOK = "gateway: test register"

func httpScope(cfg *bootstrapv1.BootstrapConfig, decl *appkit.Decl) appkit.ApplyScope {
	return appkit.ApplyScope{Config: cfg, Decl: decl}
}

func httpServerCfg(driver string) *bootstrapv1.BootstrapConfig {
	return &bootstrapv1.BootstrapConfig{Server: &bootstrapv1.Server{
		Http: &bootstrapv1.Server_Http{Addr: "127.0.0.1:0", Driver: driver},
		Grpc: &bootstrapv1.Server_Grpc{Addr: "127.0.0.1:19090"},
	}}
}

func stubRegister(context.Context, *grpc.ClientConn) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(gwRegisterOK)) })
	return mux, nil
}

func TestHTTPProvider_SegmentsAndPlan(t *testing.T) {
	p := HTTPProvider(stubRegister)
	if segs := p.Segments(); len(segs) != 1 || segs[0] != "server.http" {
		t.Fatalf("Segments() = %v, want [server.http]", segs)
	}
	nodes, err := p.Plan(context.Background(), appkit.PlanScope{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("Plan nodes = %d, want 1", len(nodes))
	}
	n := nodes[0]
	// 与 std HTTP 面同 Key：接管靠段路径末者胜 eviction，同键才不会被
	// 计划里留下两个争同一地址的 server 节点。
	if n.Key != "server.http" || n.Kind != "server" || n.Phase != appkit.PhaseServe {
		t.Fatalf("node = %+v, want server.http/server/serve", n)
	}
	if !n.Opaque || n.Reload != appkit.ReloadStatic {
		t.Fatalf("node %+v: transcoding routes are closures (Opaque), face is static", n)
	}
}

// 产物是 *gateway.GatewayServer：面与 TLS 取 server.http，上游取 server.grpc。
func TestHTTPProvider_BuildConstructsGatewayOnHTTPPort(t *testing.T) {
	srv, cleanup, err := HTTPProvider(stubRegister).Build(context.Background(),
		httpScope(httpServerCfg(DriverGRPCGateway), &appkit.Decl{}), appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil (conn lifecycle belongs to Start/Stop)")
	}
	gw, ok := srv.(*gateway.GatewayServer)
	if !ok {
		t.Fatalf("srv = %T, want *gateway.GatewayServer", srv)
	}
	stop := startAndWait(t, gw)
	defer stop()
	body := httpGet(t, gw.Endpoint()+"/anything")
	if body != gwRegisterOK {
		t.Fatalf("business route = %q, want the registered handler (face is the transcoding mux)", body)
	}
}

// driver 留空 = grpc-gateway 同义；非转码的显式值说明能力接在不消费它的
// 面上，构造期失败而非静默降级。
func TestHTTPProvider_DriverGate(t *testing.T) {
	if _, _, err := HTTPProvider(stubRegister).Build(context.Background(),
		httpScope(httpServerCfg(""), &appkit.Decl{}), appkit.Node{}); err != nil {
		t.Fatalf("Build with empty driver: %v, want success", err)
	}
	_, _, err := HTTPProvider(stubRegister).Build(context.Background(),
		httpScope(httpServerCfg("gin"), &appkit.Decl{}), appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "grpc-gateway") {
		t.Fatalf("err = %v, want driver mismatch fail-fast naming grpc-gateway", err)
	}
}

func TestHTTPProvider_RegisterNilFailsFast(t *testing.T) {
	_, _, err := HTTPProvider(nil).Build(context.Background(),
		httpScope(httpServerCfg(""), &appkit.Decl{}), appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "nil register func") {
		t.Fatalf("err = %v, want nil register fail-fast", err)
	}
}

// 段缺失的两条判据在引擎侧不可达（Provider 未触发即跳过），故直接打在 Build
// 上：server.http 为 nil 是声明与契约不匹配，server 段为 nil 是引擎 bug。
func TestHTTPProvider_MissingSectionGates(t *testing.T) {
	_, _, err := HTTPProvider(stubRegister).Build(context.Background(),
		httpScope(&bootstrapv1.BootstrapConfig{Server: &bootstrapv1.Server{}}, &appkit.Decl{}), appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "server.http is nil") {
		t.Fatalf("err = %v, want nil http section fail-fast", err)
	}
	_, _, err = HTTPProvider(stubRegister).Build(context.Background(),
		httpScope(&bootstrapv1.BootstrapConfig{}, &appkit.Decl{}), appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "report this as a bug") {
		t.Fatalf("err = %v, want bug report", err)
	}
}

// WithHealth 声明时双探针包在转码 handler 外层：转码路由与 /healthz、/readyz
// 同一 mux，业务路由仍让位给精确探针路径。
//
// 两探针分别钉住语义差：liveness 恒 200，readiness 取 Decl.Health 聚合结果
// ——挂了 Down 检查器后 /readyz 必须 503（证明包装读的是健康源而非硬编码）。
func TestHTTPProvider_ProbesWrappedOnTranscodingFace(t *testing.T) {
	h := health.New()
	srv, _, err := HTTPProvider(stubRegister).Build(context.Background(),
		httpScope(httpServerCfg(""), &appkit.Decl{Health: h, ProbeLivePath: "/healthz", ProbeReadyPath: "/readyz"}),
		appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	gw := srv.(*gateway.GatewayServer)
	stop := startAndWait(t, gw)
	defer stop()
	addr := "http://" + strings.TrimPrefix(gw.Endpoint(), "http://")

	if code := httpStatus(t, addr+"/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200 (liveness is a constant)", code)
	}
	if code := httpStatus(t, addr+"/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz = %d, want 200 with no checkers", code)
	}
	h.Register("dep", health.PingFunc(func(context.Context) error { return errors.New("dep down") }))
	if code := httpStatus(t, addr+"/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d, want 503 (readiness must follow the health source)", code)
	}
	if code := httpStatus(t, addr+"/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200 while not ready", code)
	}
	if body := httpGet(t, addr+"/anything"); body != gwRegisterOK {
		t.Fatalf("business route = %q, want the registered handler under the probe mux", body)
	}
}

// Apply 落声明的是携带 register 的接收者本身（零值副本会让 Build 直呼 nil）。
func TestHTTPProvider_ApplyPreservesRegister(t *testing.T) {
	d := &appkit.Decl{}
	HTTPProvider(stubRegister).Apply(d)
	if len(d.Providers) != 1 {
		t.Fatalf("decl providers = %d, want 1", len(d.Providers))
	}
	srv, _, err := d.Providers[0].Build(context.Background(),
		httpScope(httpServerCfg(""), &appkit.Decl{}), appkit.Node{})
	if err != nil {
		t.Fatalf("Build via decl: %v", err)
	}
	if srv == nil {
		t.Fatal("srv = nil, register was lost in Apply")
	}
}

// startAndWait 启动面服务器并等待 Endpoint 就绪（:0 场景），返回 stop。
func startAndWait(t *testing.T, srv interface {
	Start(context.Context) error
	Stop(context.Context) error
	Endpoint() string
}) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()

	deadline := time.After(3 * time.Second)
	for {
		if ep := srv.Endpoint(); ep != "" && !strings.HasSuffix(ep, ":0") {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatal("Endpoint not ready in time")
		case err := <-done:
			cancel()
			t.Fatalf("Start returned early: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	return func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCancel()
		_ = srv.Stop(stopCtx)
		cancel()
		<-done
	}
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 (body %q)", url, resp.StatusCode, data)
	}
	return string(data)
}

func httpStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}
