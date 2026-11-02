package contract

// discovery_test.go —— 段驱动装配的发现接线：消费方只改 backend_grpc_addr。
//
// 锁定的不变量：
//  1. 地址写 discovery:/// + 注册中心已装配（App 面 = registry 段产物的落点，
//     或 Decl 上的显式实例）→ 拨号真的用解析出的端点；
//  2. 地址写 discovery:/// 而注册中心缺席、或只会注册不会发现 → Start 失败并
//     说明原因，不交给 gRPC 的 dns 兜底去拨一个必然拨不通的名字；
//  3. 直连地址不受本接线影响（没有注册中心也照常起）；
//  4. 调用方显式注入的 WithDiscovery 优先，且此时不要求装配面上另有注册中心。
//
// 验活用真实 grpc.Server + inmemory 注册中心 + 127.0.0.1:0 监听，断言的是
// 「后端连接 Ready」——解析与拨号两端都接通才可能成立。

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/registry"
	"github.com/kalandramo/bald/registry/inmemory"
	gateway "github.com/kalandramo/bald/transport/gateway"
)

const discoveryBackendTarget = gateway.DiscoveryScheme + ":///backend"

// registrarOnly 只实现 registry.Registrar：装配了半个能力的注册中心接不上发现
// 地址时必须说清楚。
type registrarOnly struct{}

func (registrarOnly) Register(context.Context, *registry.ServiceInstance) error { return nil }
func (registrarOnly) Deregister(context.Context, *registry.ServiceInstance) error {
	return nil
}

func gatewayScope(backend string, decl *appkit.Decl, app *appkit.App) appkit.ApplyScope {
	return appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{Server: &bootstrapv1.Server{
			Gateway: &bootstrapv1.Server_Gateway{Addr: "127.0.0.1:0", BackendGrpcAddr: backend},
		}},
		Decl: decl,
		App:  app,
	}
}

// newConnSink 接住 gateway 交给转码回调的拨号句柄（转码内容由 gateway 侧覆盖）。
func newConnSink() (RegisterFunc, func() *grpc.ClientConn) {
	var (
		mu   sync.Mutex
		conn *grpc.ClientConn
	)
	return func(_ context.Context, c *grpc.ClientConn) (http.Handler, error) {
			mu.Lock()
			conn = c
			mu.Unlock()
			return nil, nil
		}, func() *grpc.ClientConn {
			mu.Lock()
			defer mu.Unlock()
			return conn
		}
}

func startGRPCServer(t *testing.T) (net.Listener, *grpc.Server) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	go func() { _ = srv.Serve(lis) }()
	return lis, srv
}

// registerBackend 把 gRPC 服务器的监听端点注册进内存注册中心。
func registerBackend(t *testing.T, addr string) *inmemory.Registrar {
	t.Helper()
	d := inmemory.New()
	if err := d.Register(context.Background(), &registry.ServiceInstance{
		ID: "b1", Name: "backend", Endpoints: []string{addr},
	}); err != nil {
		t.Fatal(err)
	}
	return d
}

// startAndWaitConn 后台 Start（期望成功），返回转码回调拿到的后端连接。
// Start 提前返回错误时把那个错误报出来——期望失败的用例走 [buildAndStartError]。
func startAndWaitConn(t *testing.T, p appkit.Provider, scope appkit.ApplyScope, connOf func() *grpc.ClientConn) *grpc.ClientConn {
	t.Helper()
	_, gw := buildGateway(t, p, scope)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- gw.Start(ctx) }()
	t.Cleanup(func() {
		_ = gw.Stop(context.Background())
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Start: %v", err)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c := connOf(); c != nil {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("Start 提前返回: %v", err)
	default:
	}
	t.Fatal("Start 未把后端连接交给转码回调")
	return nil
}

// buildGateway 走 Build 并断言产物类型。
func buildGateway(t *testing.T, p appkit.Provider, scope appkit.ApplyScope) (any, *gateway.GatewayServer) {
	t.Helper()
	srv, _, err := p.Build(context.Background(), scope, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	gw, ok := srv.(*gateway.GatewayServer)
	if !ok {
		t.Fatalf("产物 = %T, want *gateway.GatewayServer", srv)
	}
	return srv, gw
}

// buildAndStartError 期望 Start 在建连前失败，返回该错误。
func buildAndStartError(t *testing.T, p appkit.Provider, scope appkit.ApplyScope) error {
	t.Helper()
	_, gw := buildGateway(t, p, scope)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- gw.Start(ctx) }()
	select {
	case err := <-done:
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCancel()
		_ = gw.Stop(stopCtx)
		return err
	case <-time.After(3 * time.Second):
		_ = gw.Stop(context.Background())
		<-done
		t.Fatal("Start 没有失败（已进入服务），want 建连前失败")
		return nil
	}
}

func waitReady(t *testing.T, conn *grpc.ClientConn, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if conn.GetState().String() == "READY" {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// 地址写 discovery:///，注册中心在 App 面上（registry 段产物的落点）→ 真拨到解析出的端点。
func TestAppProviderDiscoveryAddrUsesAssembledRegistrar(t *testing.T) {
	lis, grpcSrv := startGRPCServer(t)
	defer grpcSrv.Stop()

	register, connOf := newConnSink()
	scope := gatewayScope(discoveryBackendTarget, &appkit.Decl{}, appkit.NewApp(appkit.Registrar(registerBackend(t, lis.Addr().String()))))

	conn := startAndWaitConn(t, AppProvider(register), scope, connOf)
	conn.Connect()
	if !waitReady(t, conn, 3*time.Second) {
		t.Fatalf("装配好的注册中心没接上：后端连接未 Ready（state=%v）", conn.GetState())
	}
}

// 同一条地址、显式实例挂在 Decl 上（无 App 的调用面）同样接得上。
func TestAppProviderDiscoveryAddrFromDeclRegistrar(t *testing.T) {
	lis, grpcSrv := startGRPCServer(t)
	defer grpcSrv.Stop()

	register, connOf := newConnSink()
	scope := gatewayScope(discoveryBackendTarget, &appkit.Decl{Registrar: registerBackend(t, lis.Addr().String())}, nil)

	conn := startAndWaitConn(t, AppProvider(register), scope, connOf)
	conn.Connect()
	if !waitReady(t, conn, 3*time.Second) {
		t.Fatalf("Decl.Registrar 没被当作回退来源（state=%v）", conn.GetState())
	}
}

// 没装配注册中心 → 失败要说清楚缺什么，而不是静默拨一个坏目标。
func TestAppProviderDiscoveryAddrWithoutRegistrarFailsFast(t *testing.T) {
	err := buildAndStartError(t, AppProvider(stubRegister), gatewayScope(discoveryBackendTarget, &appkit.Decl{}, nil))
	if err == nil || !strings.Contains(err.Error(), "no registry is assembled") {
		t.Fatalf("Start err = %v, want 说明未装配注册中心", err)
	}
}

// 装配的注册中心只会注册（无发现半边）→ 同样启动期失败，并点名具体类型。
func TestAppProviderDiscoveryAddrWithRegistrarOnlyFailsFast(t *testing.T) {
	scope := gatewayScope(discoveryBackendTarget, &appkit.Decl{}, appkit.NewApp(appkit.Registrar(registrarOnly{})))
	err := buildAndStartError(t, AppProvider(stubRegister), scope)
	if err == nil || !strings.Contains(err.Error(), "does not implement registry.Discovery") {
		t.Fatalf("Start err = %v, want 点名缺少发现半边", err)
	}
}

// 直连配置不受接线影响：没有注册中心也照常起、照常转码。
func TestAppProviderDirectAddrNeedsNoRegistrar(t *testing.T) {
	_, gw := buildGateway(t, AppProvider(stubRegister), gatewayScope("127.0.0.1:19090", &appkit.Decl{}, nil))
	stop := startAndWait(t, gw)
	defer stop()
	if body := httpGet(t, gw.Endpoint()+"/anything"); body != gwRegisterOK {
		t.Fatalf("转码面响应 = %q, want 业务 handler 内容", body)
	}
}

// 显式注入 Discovery 的用法（接线前的唯一通路）不得因为自动来源而要求装配面上
// 另有注册中心——否则自带客户端的消费者被这次接线打回原形。
func TestAppProviderExplicitDiscoveryNeedsNoAssembledRegistrar(t *testing.T) {
	lis, grpcSrv := startGRPCServer(t)
	defer grpcSrv.Stop()

	register, connOf := newConnSink()
	scope := gatewayScope(discoveryBackendTarget, &appkit.Decl{}, nil) // 无 App、Decl 无 Registrar

	conn := startAndWaitConn(t, AppProvider(register, gateway.WithDiscovery(registerBackend(t, lis.Addr().String()))), scope, connOf)
	conn.Connect()
	if !waitReady(t, conn, 3*time.Second) {
		t.Fatalf("显式 WithDiscovery 未生效（state=%v）", conn.GetState())
	}
}

// HTTPProvider 的上游就是本进程 gRPC 面自己的绑定地址，不接自动来源：那里写
// discovery:/// 是配置错误，报错应指向「没有发现来源」而不是「没装配注册中心」
// （证明这根线确实没接上，且装配了注册中心也不会被误用）。
func TestHTTPProviderDoesNotAutoDeriveDiscovery(t *testing.T) {
	cfg := &bootstrapv1.BootstrapConfig{Server: &bootstrapv1.Server{
		Http: &bootstrapv1.Server_Http{Addr: "127.0.0.1:0"},
		Grpc: &bootstrapv1.Server_Grpc{Addr: discoveryBackendTarget},
	}}
	scope := appkit.ApplyScope{Config: cfg, Decl: &appkit.Decl{Registrar: inmemory.New()}}
	err := buildAndStartError(t, HTTPProvider(stubRegister), scope)
	if err == nil || !strings.Contains(err.Error(), "no discovery is available") {
		t.Fatalf("Start err = %v, want 无发现来源的 fail-fast（证明 HTTPProvider 没接自动来源）", err)
	}
}
