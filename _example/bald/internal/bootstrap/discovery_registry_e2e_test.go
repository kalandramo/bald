//go:build !grpcgw

// 真注册中心端到端（opt-in）：把 discovery:/// 这条路接到**生产后端**上。
//
// 内存注册中心（[TestGatewayBackendViaDiscovery]）证明的是装配与派生；这里证明的是
// 这份接线赖以工作、单测替不了的后端语义：
//
//  1. 全量快照推送：注册后 Watch 的 Next 真把新列表交到 ClientConn——gRPC 连接因此
//     从 503 翻到 200，不需要重启网关；注销后快照为空也不清空已发布地址。
//  2. 真存储交回的端点形态：bald 的 gRPC 面注册的是 `grpc://host:port`，同实例还带
//     `http://host:port`（见 transport/grpc/grpc_server.go 与 HTTPServer.Endpoint）；
//     nacos 更绕——它按 endpoint 的 scheme 把同一实例拆成 `<服务名>.<scheme>` 两个服务
//     存，取回时再用 metadata 的 kind 拼回 `<scheme>://ip:port`。原样交给 gRPC 必然
//     `too many colons in address`，服务名写错形态则一个实例都拿不到。
//
// 后端不可达即 Skip：那是环境缺失，不是验证失败。
//
//	cd _example/bald
//	go test ./internal/bootstrap/ -run TestGatewayDiscoveryEtcd  -v                    # 需 etcd
//	go test ./internal/bootstrap/ -run 'TestGatewayDiscoveryNacos|TestNacosDiscovery' -v  # 需 nacos
//
//	# etcd:  docker run -d -p 127.0.0.1:2379:2379 quay.io/coreos/etcd:v3.5.14 /usr/local/bin/etcd \
//	#          --advertise-client-urls http://127.0.0.1:2379 --listen-client-urls http://0.0.0.0:2379
//	# nacos: docker run -d -p 127.0.0.1:8848:8848 -p 127.0.0.1:9848:9848 \
//	#          -e MODE=standalone -e NACOS_AUTH_ENABLE=false nacos/nacos-server:v2.3.2
//	#          # v2.3.2 无 arm64 manifest：Apple Silicon 加 --platform linux/amd64（模拟下验过）
//	# 地址分别用 BALD_E2E_ETCD_ADDR / BALD_E2E_NACOS_ADDR 覆盖（默认 127.0.0.1:2379 / :8848）。
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/registry"
	etcdcontract "github.com/kalandramo/bald/registry/etcd/contract"
	nacoscontract "github.com/kalandramo/bald/registry/nacos/contract"
	gateway "github.com/kalandramo/bald/transport/gateway"

	baldv1 "github.com/kalandramo/bald/example/bald/api/gen"
)

// registryGreeter 是本用例自带的 gRPC 后端。响应里的 greeting 用来确认请求打到了
// 这套端点上，而不是注册中心里残留的同名实例。
type registryGreeter struct {
	baldv1.UnimplementedGreetServiceServer
}

func (registryGreeter) Greet(_ context.Context, req *baldv1.GreetRequest) (*baldv1.GreetResponse, error) {
	return &baldv1.GreetResponse{Greet: "hello, " + req.GetName()}, nil
}

// discoveryReg 是一个后端接进网关所需的最小面：注册半边 + 解析半边。
type discoveryReg struct {
	reg registry.Registrar
	d   registry.Discovery
}

// prodDiscovery 把 Registrar 收成 Discovery。断言失败即说明该后端只实现了注册半边，
// 而 discovery:/// 这条路在装配期本就该 fail-fast（见 transport/gateway/contract）。
func prodDiscovery(t *testing.T, reg registry.Registrar) discoveryReg {
	t.Helper()
	d, ok := reg.(registry.Discovery)
	if !ok {
		t.Fatalf("生产注册中心 %T 未实现 registry.Discovery", reg)
	}
	return discoveryReg{reg: reg, d: d}
}

// etcdDiscovery 按契约段构造 etcd 注册中心（与业务装配同一条构造路径）。
func etcdDiscovery(t *testing.T, ctx context.Context) discoveryReg {
	t.Helper()
	if testing.Short() {
		t.Skip("集成测试：依赖真实 etcd，-short 下跳过（与 registry/etcd 的同源约定）")
	}
	addr := envOr("BALD_E2E_ETCD_ADDR", "127.0.0.1:2379")
	reg, closeReg, err := etcdcontract.Provider(ctx, &bootstrapv1.Registry{
		Type: etcdcontract.Type,
		Etcd: &bootstrapv1.Registry_Etcd{Endpoints: []string{addr}},
	})
	if err != nil {
		t.Skipf("etcd 注册中心构造失败（环境缺失，非验证失败）: %v", err)
	}
	t.Cleanup(closeReg)
	dr := prodDiscovery(t, reg)
	// 探活：etcd client 惰性建连，构造成功不代表连上；而缺失服务名返回空集不报错，
	// 所以这里的错误只可能来自连接本身。
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer probeCancel()
	if _, err := dr.d.GetService(probeCtx, uniqueName("probe")); err != nil {
		t.Skipf("etcd %s 不可达，跳过（环境缺失）: %v", addr, err)
	}
	return dr
}

// nacosDiscovery 按契约段构造 nacos 注册中心。
func nacosDiscovery(t *testing.T, ctx context.Context) discoveryReg {
	t.Helper()
	if testing.Short() {
		t.Skip("集成测试：依赖真实 nacos，-short 下跳过")
	}
	addr := envOr("BALD_E2E_NACOS_ADDR", "127.0.0.1:8848")
	// 探活用 TCP：nacos 的 SelectInstances 对「服务不存在」也返回错误，拿它探活会把
	// 服务未注册误判成后端不可达。
	probe, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Skipf("nacos %s 不可达，跳过（环境缺失）: %v", addr, err)
	}
	_ = probe.Close()
	reg, closeReg, err := nacoscontract.Provider(ctx, &bootstrapv1.Registry{
		Type:  nacoscontract.Type,
		Nacos: &bootstrapv1.Registry_Nacos{ServerAddrs: []string{addr}},
	})
	if err != nil {
		t.Skipf("nacos 注册中心构造失败（环境缺失，非验证失败）: %v", err)
	}
	t.Cleanup(closeReg)
	return prodDiscovery(t, reg)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// uniqueName 让多次运行、以及共用一个后端实例的多个用例互不干扰。
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// runDiscoveryRound 跑后端无关的一轮：后端未注册时网关明确不可用 → 注册后由 Watch
// 推进活连接变 200 → 注销后仍打到旧地址 → 后端下线后失败。
// service 是实例注册名，target 是网关解析的服务名（nacos 下两者不同，见命名口径用例）。
func runDiscoveryRound(t *testing.T, dr discoveryReg, service, target string, deadline time.Duration) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gw := startDiscoveryGateway(t, ctx, dr.d, target)
	hostPort := discoveryGatewayAddr(t, gw)

	// ① 后端还没注册：解析不到实例不该让网关崩，也不该静默成功。
	if code, body := postGreet(t, hostPort, `{"name":"before"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("未注册时期望 503，得到 %d %s", code, body)
	}

	// ② 起真后端并注册。端点顺序刻意把「无人监听的 http 面」排在 gRPC 面前：resolver
	// 若不过滤 scheme，pick_first 先拨死地址、再拨 `grpc://…`（too many colons），
	// REST 永远停在 503。
	lis, srv := startRegistryBackend(t)
	inst := &registry.ServiceInstance{
		ID:      service + "-1",
		Name:    service,
		Version: "v1",
		Endpoints: []string{
			"http://" + freeAddr(t),         // 同实例的 HTTP 面：不该被当成 gRPC 后端
			"grpc://" + lis.Addr().String(), // 与 transport/grpc 注册时的形态一致
		},
	}
	if err := dr.reg.Register(ctx, inst); err != nil {
		t.Fatalf("register %s: %v", service, err)
	}
	defer func() { _ = dr.reg.Deregister(context.Background(), inst) }()

	code, body := postGreetUntil(t, hostPort, `{"name":"registry"}`, http.StatusOK, deadline)
	if code != http.StatusOK || !strings.Contains(body, "hello, registry") {
		t.Fatalf("注册后 REST 未打通（Watch 推送或端点过滤失效）= %d %s", code, body)
	}

	// ③ 注销。钉住刻意选择的行为——空快照**不**下发 UpdateState：gRPC 对空地址集会回
	// ErrBadResolverState 并连已有健康地址一起清掉，所以 resolver 只 ReportError 保订阅。
	// 表现就是网关仍持有刚注销的地址；后端进程还活着则 REST 仍 200。
	if err := dr.reg.Deregister(ctx, inst); err != nil {
		t.Fatalf("deregister %s: %v", service, err)
	}
	time.Sleep(500 * time.Millisecond) // 给删除事件推到位的时间
	if code, body := postGreet(t, hostPort, `{"name":"registry"}`); code != http.StatusOK {
		t.Fatalf("注销不应清空已发布地址: %d %s", code, body)
	}

	// ④ 后端下线：地址没了、连接也断了，RPC 必须失败而不是静默返回旧数据。
	srv.Stop()
	if code, body := postGreetUntil(t, hostPort, `{"name":"registry"}`, http.StatusServiceUnavailable, 15*time.Second); code != http.StatusServiceUnavailable {
		t.Fatalf("后端下线后期望 503，得到 %d %s", code, body)
	}
}

// TestGatewayDiscoveryEtcdWatchPush 在真 etcd 上跑完整一轮。
//
// etcd 按 ServiceInstance.Name 原样存 key，服务名就是注册时那个名字，所以注册的实例
// 与网关解析的目标同名。
func TestGatewayDiscoveryEtcdWatchPush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dr := etcdDiscovery(t, ctx)

	service := uniqueName("bald-etcd-gw")
	runDiscoveryRound(t, dr, service, service, 15*time.Second)
}

// TestGatewayDiscoveryNacosWatchPush 在真 nacos 上跑完整一轮，解析目标名带 `.grpc`
// 后缀（口径见 TestNacosDiscoveryNameCarriesSchemeSuffix）。
//
// deadline 给得宽：服务还不存在时 GetService 直接报错，resolver 的 Build 随之失败，
// gRPC 按退避重建 resolver（最长间隔 12s），首跳因此可能等上一整个退避周期。
func TestGatewayDiscoveryNacosWatchPush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dr := nacosDiscovery(t, ctx)

	service := uniqueName("bald-nacos-gw")
	runDiscoveryRound(t, dr, service, service+".grpc", 45*time.Second)
}

// TestNacosDiscoveryNameCarriesSchemeSuffix 钉住 nacos 的命名口径——它是「消费方只改
// 配置」这句话在 nacos 上的实际形态：`server.gateway.backend_grpc_addr` 得写
// `discovery:///<服务名>.grpc`，裸服务名解析不到实例。
//
// 这是后端实现的事实（Register 逐 endpoint 建 `<name>.<scheme>` 服务，GetService 按
// 传入名原样查），不是 resolver 的 bug；换 etcd/consul 后缀规则不同，所以只在 nacos
// 用例里断言，不进通用路径。
func TestNacosDiscoveryNameCarriesSchemeSuffix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dr := nacosDiscovery(t, ctx)

	service := uniqueName("bald-nacos-name")
	lis, srv := startRegistryBackend(t)
	defer srv.Stop()
	inst := &registry.ServiceInstance{
		ID:        service + "-1",
		Name:      service,
		Endpoints: []string{"http://127.0.0.1:1", "grpc://" + lis.Addr().String()},
	}
	if err := dr.reg.Register(ctx, inst); err != nil {
		t.Fatalf("register %s: %v", service, err)
	}
	t.Cleanup(func() {
		deregCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = dr.reg.Deregister(deregCtx, inst)
	})

	// 「服务不存在」在 nacos 上是错误而非空集，两者对网关等价：都拿不到可拨地址。
	liveIn := func(name string) int {
		t.Helper()
		items, err := dr.d.GetService(ctx, name)
		if err != nil {
			return 0
		}
		return len(items)
	}
	if n := liveIn(service); n != 0 {
		t.Fatalf("裸服务名 %s 在 nacos 上不该解析到实例，得到 %d 个", service, n)
	}
	items, err := dr.d.GetService(ctx, service+".grpc")
	if err != nil {
		t.Fatalf("get %s.grpc: %v", service, err)
	}
	if len(items) != 1 {
		t.Fatalf("%s.grpc 应解析到 1 个实例，得到 %d 个", service, len(items))
	}
	// 交回的端点仍带 scheme（由 metadata 的 kind 拼回），所以 resolver 剥前缀那一步
	// 在 nacos 上不是可选的。
	if ep := items[0].Endpoints[0]; !strings.HasPrefix(ep, "grpc://") {
		t.Fatalf("%s.grpc 的端点形态变了（resolver 的过滤前提失效）: %q", service, ep)
	}
}

// startRegistryBackend 起一个只实现 Greet 的真实 gRPC 后端（:0 动态端口）。
func startRegistryBackend(t *testing.T) (net.Listener, *grpc.Server) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen backend: %v", err)
	}
	srv := grpc.NewServer()
	baldv1.RegisterGreetServiceServer(srv, registryGreeter{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis, srv
}

// startDiscoveryGateway 用生产注册中心的 Discovery 起转码面：后端地址写服务名，
// 不写 host:port。监听 :0，真实端口经 Endpoint() 取。
func startDiscoveryGateway(t *testing.T, ctx context.Context, d registry.Discovery, service string) *gateway.GatewayServer {
	t.Helper()
	gw, err := gateway.NewGatewayServer(
		&bootstrapv1.Server_Http{Addr: "127.0.0.1:0"},
		&bootstrapv1.Server_Grpc{Addr: gateway.DiscoveryScheme + ":///" + service},
		registerGreet,
		gateway.WithDiscovery(d),
	)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- gw.Start(ctx) }()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = gw.Stop(stopCtx)
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("gateway Start: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("gateway 未在 5s 内退出")
		}
	})
	return gw
}

// discoveryGatewayAddr 等 Start 真建好监听再取 host:port：Endpoint() 在未监听前会
// 回落到配置里的 `127.0.0.1:0`，拿它去发请求等于测了个不存在的端口。
func discoveryGatewayAddr(t *testing.T, gw *gateway.GatewayServer) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ep := gw.Endpoint()
		if hostPort, ok := strings.CutPrefix(ep, "http://"); ok && !strings.HasSuffix(hostPort, ":0") {
			return hostPort
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("gateway 未在 5s 内开始监听（Endpoint=%s）", gw.Endpoint())
	return ""
}

// postGreetUntil 反复打到出现 want 状态码为止；超时返回最后一次结果供断言。
func postGreetUntil(t *testing.T, hostPort, payload string, want int, timeout time.Duration) (int, string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var code int
	var body string
	for time.Now().Before(deadline) {
		code, body = postGreet(t, hostPort, payload)
		if code == want {
			return code, body
		}
		time.Sleep(100 * time.Millisecond)
	}
	return code, body
}
