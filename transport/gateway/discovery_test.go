package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/serviceconfig"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/registry"
	"github.com/kalandramo/bald/registry/inmemory"
)

// fakeClientConn 把 gRPC 传给 resolver 的 ClientConn 换成可断言的记录器。
type fakeClientConn struct {
	mu      sync.Mutex
	states  []resolver.State
	errs    []error
	updates chan struct{}
}

func newFakeClientConn() *fakeClientConn {
	return &fakeClientConn{updates: make(chan struct{}, 8)}
}

func (f *fakeClientConn) UpdateState(s resolver.State) error {
	f.mu.Lock()
	f.states = append(f.states, s)
	f.mu.Unlock()
	select {
	case f.updates <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeClientConn) ReportError(err error) {
	f.mu.Lock()
	f.errs = append(f.errs, err)
	f.mu.Unlock()
}

// errList 暴露记录到的错误供断言。
func (f *fakeClientConn) errList() []error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]error(nil), f.errs...)
}

func (f *fakeClientConn) NewAddress([]resolver.Address) {}

func (f *fakeClientConn) ParseServiceConfig(string) *serviceconfig.ParseResult {
	return &serviceconfig.ParseResult{}
}

// addrStrings 取第 n 次 UpdateState 的地址列表。
func (f *fakeClientConn) addrStrings(t *testing.T, n int) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.states) <= n {
		t.Fatalf("只有 %d 次 UpdateState，取第 %d 次", len(f.states), n+1)
	}
	out := make([]string, 0, len(f.states[n].Addresses))
	for _, a := range f.states[n].Addresses {
		out = append(out, a.Addr)
	}
	return out
}

func (f *fakeClientConn) count() (states, errs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.states), len(f.errs)
}

// waitFor 等到第 want 次 UpdateState 落地。带界：被测实现静默时必须失败，
// 而不是把测试拖到 go test 的全局超时。
func (f *fakeClientConn) waitFor(t *testing.T, want int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if s, _ := f.count(); s >= want {
			return
		}
		select {
		case <-f.updates:
		case <-time.After(20 * time.Millisecond):
		}
	}
	s, e := f.count()
	t.Fatalf("等待第 %d 次 UpdateState 超时（states=%d errs=%v）", want, s, e)
}

func inst(id, name string, eps ...string) *registry.ServiceInstance {
	return &registry.ServiceInstance{ID: id, Name: name, Endpoints: eps}
}

// build 用真实 gRPC 的 target 解析路径构造 resolver.Target。
func build(t *testing.T, d registry.Discovery, target string) (resolver.Resolver, *fakeClientConn, error) {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse target %q: %v", target, err)
	}
	cc := newFakeClientConn()
	r, err := newDiscoveryBuilder(d).Build(resolver.Target{URL: *u}, cc, resolver.BuildOptions{})
	return r, cc, err
}

func mustBuild(t *testing.T, d registry.Discovery, target string) (resolver.Resolver, *fakeClientConn) {
	t.Helper()
	r, cc, err := build(t, d, target)
	if err != nil {
		t.Fatalf("Build(%s): %v", target, err)
	}
	t.Cleanup(r.Close)
	return r, cc
}

func TestDiscoveryResolverPublishesInitialSnapshot(t *testing.T) {
	d := inmemory.New()
	ctx := context.Background()
	// 先注册两个实例再 Build：解析必须立刻看到它们，而不是等下一次变更。
	for _, in := range []*registry.ServiceInstance{inst("a1", "svc", "127.0.0.1:9001"), inst("a2", "svc", "127.0.0.1:9002")} {
		if err := d.Register(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	_, cc := mustBuild(t, d, DiscoveryScheme+":///svc")
	cc.waitFor(t, 1, time.Second)
	if got := cc.addrStrings(t, 0); strings.Join(got, ",") != "127.0.0.1:9001,127.0.0.1:9002" {
		t.Fatalf("initial addresses = %v", got)
	}
	if _, errs := cc.count(); errs != 0 {
		t.Fatalf("正常解析不该上报错误: %v", errs)
	}
}

func TestDiscoveryResolverFollowsChanges(t *testing.T) {
	d := inmemory.New()
	ctx := context.Background()
	if err := d.Register(ctx, inst("a1", "svc", "127.0.0.1:9001")); err != nil {
		t.Fatal(err)
	}
	_, cc := mustBuild(t, d, DiscoveryScheme+":///svc")
	cc.waitFor(t, 1, time.Second)

	if err := d.Register(ctx, inst("a2", "svc", "127.0.0.1:9002")); err != nil {
		t.Fatal(err)
	}
	cc.waitFor(t, 2, time.Second)
	if got := cc.addrStrings(t, 1); len(got) != 2 || got[1] != "127.0.0.1:9002" {
		t.Fatalf("after register = %v, want 新实例入列", got)
	}

	if err := d.Deregister(ctx, inst("a1", "svc", "127.0.0.1:9001")); err != nil {
		t.Fatal(err)
	}
	cc.waitFor(t, 3, time.Second)
	if got := cc.addrStrings(t, 2); strings.Join(got, ",") != "127.0.0.1:9002" {
		t.Fatalf("after deregister = %v", got)
	}
}

// 后端尚未注册（实例为 0）是启动期的正常状态：不能对空地址集 UpdateState
// （gRPC 回 ErrBadResolverState 且会清掉已有健康地址），要 ReportError 并保持订阅。
func TestDiscoveryResolverEmptySetReportsErrorAndKeepsWatching(t *testing.T) {
	d := inmemory.New()
	_, cc := mustBuild(t, d, DiscoveryScheme+":///svc")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, errs := cc.count(); errs > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	states, errs := cc.count()
	if errs == 0 {
		t.Fatal("零实例必须上报「无实例」错误")
	}
	if states != 0 {
		t.Fatalf("零实例不得 UpdateState，got %d 次", states)
	}
	if err := d.Register(context.Background(), inst("a1", "svc", "127.0.0.1:9001")); err != nil {
		t.Fatal(err)
	}
	cc.waitFor(t, 1, time.Second) // 订阅仍在，实例出现后自动生效
	if got := cc.addrStrings(t, 0); strings.Join(got, ",") != "127.0.0.1:9001" {
		t.Fatalf("late instance = %v", got)
	}
}

// 真实注册形态的端点是带 scheme 的（`transport/grpc` 注册 "grpc://host:port"，
// `transport/http` 注册 "http://host:port"）。gRPC 的拨号器只吃 host:port：
// scheme 前缀会让 transport 创建直接失败（"unsupported scheme"），而把同实例的
// gin 面当 gRPC 后端拨是必然失败的错误。因此 grpc:// 剥前缀、其它 scheme 丢弃、
// 裸 host:port 保留（第三方注册中心里手写的实例）。
func TestDiscoveryResolverNormalizesSchemedEndpoints(t *testing.T) {
	d := inmemory.New()
	ctx := context.Background()
	// 同一实例三个面：http 在前（若不过滤，pick_first 会先拨它）。
	if err := d.Register(ctx, inst("m1", "svc",
		"http://127.0.0.1:8080", "grpc://127.0.0.1:9090", "127.0.0.1:9091")); err != nil {
		t.Fatal(err)
	}
	// 只有非 gRPC 面的实例必须整体出局，而不是留下一个拨不通的地址。
	if err := d.Register(ctx, inst("m2", "svc", "https://127.0.0.1:8443")); err != nil {
		t.Fatal(err)
	}
	_, cc := mustBuild(t, d, DiscoveryScheme+":///svc")
	cc.waitFor(t, 1, time.Second)
	if got := cc.addrStrings(t, 0); strings.Join(got, ",") != "127.0.0.1:9090,127.0.0.1:9091" {
		t.Fatalf("schemed endpoints = %v, want grpc:// 剥前缀且 http 面被丢弃", got)
	}
}

// 全是非 gRPC 面时按「无实例」上报错误并保持订阅——与零实例同一条路径，
// 不能让 ClientConn 拿到一个必然拨不通的地址。
func TestDiscoveryResolverAllNonGRPCEndpointsReportsNoInstances(t *testing.T) {
	d := inmemory.New()
	if err := d.Register(context.Background(), inst("h1", "svc", "http://127.0.0.1:8080")); err != nil {
		t.Fatal(err)
	}
	_, cc := mustBuild(t, d, DiscoveryScheme+":///svc")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, errs := cc.count(); errs > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if states, errs := cc.count(); states != 0 || errs == 0 {
		t.Fatalf("states=%d errs=%d, want 不上报地址且报「无实例」错误", states, errs)
	}
}

func TestDiscoveryResolverTargetMustCarryServiceName(t *testing.T) {
	d := inmemory.New()
	r, cc, err := build(t, d, DiscoveryScheme+":///")
	if err == nil || !strings.Contains(err.Error(), "no service name") {
		t.Fatalf("空服务名应报错, got %v", err)
	}
	if r != nil {
		t.Fatal("非法目标不得返回 resolver")
	}
	if states, _ := cc.count(); states != 0 {
		t.Fatalf("非法目标不得产出状态, got %d", states)
	}
}

// stubDiscovery 注入错误路径（inmemory 自身永不返回错误）。
type stubDiscovery struct {
	instances []*registry.ServiceInstance
	getErr    error
	watchErr  error
	calls     int
}

func (s *stubDiscovery) GetService(context.Context, string) ([]*registry.ServiceInstance, error) {
	s.calls++
	return s.instances, s.getErr
}

func (s *stubDiscovery) Watch(context.Context, string) (registry.Watcher, error) {
	s.calls++
	if s.watchErr != nil {
		return nil, s.watchErr
	}
	return blockingWatcher{}, nil
}

type blockingWatcher struct{}

func (blockingWatcher) Next(ctx context.Context) ([]*registry.ServiceInstance, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingWatcher) Stop() error { return nil }

func TestDiscoveryResolverBuildErrors(t *testing.T) {
	sentinel := errors.New("registry down")
	cases := []struct {
		name    string
		d       *stubDiscovery
		wantErr string
	}{
		// 只有「没有推送通道」才把失败交回 gRPC——重建 resolver 是唯一的恢复路径。
		// 首次快照失败不在这里：见 TestDiscoveryResolverRecoversAfterSnapshotError。
		{"Watch 失败", &stubDiscovery{
			instances: []*registry.ServiceInstance{inst("a", "svc", "127.0.0.1:1")},
			watchErr:  sentinel,
		}, "watch svc"},
	}
	for _, tc := range cases {
		r, _, err := build(t, tc.d, DiscoveryScheme+":///svc")
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("%s: Build err = %v, want 含 %q", tc.name, err, tc.wantErr)
		}
		if r != nil {
			t.Fatalf("%s: Build 失败却返回了 resolver", tc.name)
		}
	}
}

// snapshotErrDiscovery 复刻 nacos 的形状：服务还没被注册时 GetService 直接报错
// （内存与 etcd 返回空集），而 Watch 照常可用。
type snapshotErrDiscovery struct {
	registry.Discovery
}

func (snapshotErrDiscovery) GetService(context.Context, string) ([]*registry.ServiceInstance, error) {
	return nil, errors.New("instance list is empty!")
}

// 首次快照报错不能变成 Build 报错：gRPC 只在重连退避到期才重建 resolver（实测最长
// 2 分钟内一次都不重解析），后端随后注册上来也接不住。这里要求改成「上报原因 + 保留
// 订阅」，等 Watch 的推送恢复——网关先于后端启动时的唯一出路。
func TestDiscoveryResolverRecoversAfterSnapshotError(t *testing.T) {
	d := inmemory.New()
	_, cc := mustBuild(t, snapshotErrDiscovery{Discovery: d}, DiscoveryScheme+":///svc")

	if states, errs := cc.count(); states != 0 || errs != 1 {
		t.Fatalf("快照报错后 states=%d errs=%d, want 0/1（既不产状态也不能静默）", states, errs)
	}

	if err := d.Register(context.Background(), inst("a1", "svc", "127.0.0.1:9001")); err != nil {
		t.Fatal(err)
	}
	cc.waitFor(t, 1, time.Second)
	if got := cc.addrStrings(t, 0); len(got) != 1 || got[0] != "127.0.0.1:9001" {
		t.Fatalf("推送后地址 = %v, want 127.0.0.1:9001", got)
	}
}

// Close 引起的 Next 返回不是故障：误上报会让 ClientConn 平白掉进
// TransientFailure，停机路径上尤其难查。
func TestCloseDoesNotReportError(t *testing.T) {
	d := inmemory.New()
	// 先注册再 Build：避免把「零实例」那次预期内的 ReportError 混进断言。
	if err := d.Register(context.Background(), inst("a1", "svc", "127.0.0.1:9001")); err != nil {
		t.Fatal(err)
	}
	r, cc := mustBuild(t, d, DiscoveryScheme+":///svc")
	cc.waitFor(t, 1, time.Second)
	r.Close()
	r.Close() // 幂等
	time.Sleep(100 * time.Millisecond)
	if _, errs := cc.count(); errs != 0 {
		t.Fatalf("Close 后上报了错误: %v", errs)
	}
}

// --- 端到端：gRPC 真按解析出的端点建连，而不只是 resolver 内部自洽 ---

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

func TestDiscoveryTargetResolvesAndConnects(t *testing.T) {
	lis, srv := startGRPCServer(t)
	defer srv.Stop()

	d := inmemory.New()
	if err := d.Register(context.Background(), inst("b1", "svc", lis.Addr().String())); err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(
		DiscoveryScheme+":///svc",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithResolvers(newDiscoveryBuilder(d)),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = conn.Close() }()
	conn.Connect()
	if !waitReady(t, conn, 3*time.Second) {
		t.Fatalf("未进入 Ready：解析出的端点没被拨通（state=%v）", conn.GetState())
	}
}

// 注入 Discovery 不得影响直连写法：同一 ClientConn 上 host:port 仍由原生
// dns resolver 处理，Discovery 一次都不该被问。
func TestPlainAddressUnaffectedByInjectedDiscovery(t *testing.T) {
	lis, srv := startGRPCServer(t)
	defer srv.Stop()

	d := &stubDiscovery{getErr: errors.New("must not be consulted")}
	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithResolvers(newDiscoveryBuilder(d)),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = conn.Close() }()
	conn.Connect()
	if !waitReady(t, conn, 3*time.Second) {
		t.Fatalf("直连目标在注入 Discovery 后拨不通（state=%v）", conn.GetState())
	}
	if d.calls != 0 {
		t.Fatalf("直连目标却问了 Discovery %d 次", d.calls)
	}
}

// 端点指向已死地址时不得 Ready——证明上面的 Ready 来自解析结果，不是默认值。
func TestDeadEndpointDoesNotConnect(t *testing.T) {
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := lis.Addr().String()
	_ = lis.Close()

	d := inmemory.New()
	if err := d.Register(context.Background(), inst("b1", "svc", dead)); err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(
		DiscoveryScheme+":///svc",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithResolvers(newDiscoveryBuilder(d)),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = conn.Close() }()
	conn.Connect()
	if waitReady(t, conn, 400*time.Millisecond) {
		t.Fatal("死端点不应进入 Ready")
	}
}

// 契约段 backend_grpc_addr 经 WithDiscovery 走通：构造 gateway → 解析 → 拨号。
func TestGatewayServerWithDiscoveryDialsResolvedBackend(t *testing.T) {
	lis, srv := startGRPCServer(t)
	defer srv.Stop()

	d := inmemory.New()
	if err := d.Register(context.Background(), inst("b1", "backend", lis.Addr().String())); err != nil {
		t.Fatal(err)
	}
	gw := newGatewayWithBackend(t, DiscoveryScheme+":///backend", WithDiscovery(d))

	// Start 里 s.conn 的赋值在 register 返回之后，连接可见即证明转码面拿到了
	// 拨号句柄（nil 的 register 也走同一条赋值路径，不断言其内容）。
	conn := serveGateway(t, gw)
	conn.Connect()
	if !waitReady(t, conn, 3*time.Second) {
		t.Fatalf("gateway 到后端的连接未 Ready（state=%v）", conn.GetState())
	}
}

// 真实注册形态端到端：bald 的 server 交出的端点是 `grpc://host:port`（HTTP 面
// 同实例里是 `http://host:port`）。resolver 不剥 scheme 时 gRPC 建不出 transport，
// 连接永远不进 Ready——本判据把「解析出地址」和「真拨得通」之间的距离钉死。
// http 端点排在前面还要求 Ready，同时证明非 gRPC 面被丢弃而不是留给拨号器试错。
func TestGatewayServerDialsRealSchemedRegistration(t *testing.T) {
	lis, srv := startGRPCServer(t)
	defer srv.Stop()

	d := inmemory.New()
	// 端点顺序刻意把 gin 面放在 gRPC 面之前（Kind=mixed 的同进程多面实例）。
	if err := d.Register(context.Background(), inst("b1", "backend",
		"http://127.0.0.1:1", "grpc://"+lis.Addr().String())); err != nil {
		t.Fatal(err)
	}
	gw := newGatewayWithBackend(t, DiscoveryScheme+":///backend", WithDiscovery(d))
	conn := serveGateway(t, gw)
	conn.Connect()
	if !waitReady(t, conn, 3*time.Second) {
		t.Fatalf("带 scheme 的真实注册形态未拨通（state=%v）", conn.GetState())
	}
}

// 地址写了 discovery:/// 但没有任何发现来源时 Start 必须失败：gRPC 会把未注册的
// scheme 退回默认 dns resolver，静默去拨一个必然拨不通的名字。
func TestGatewayServerDiscoveryTargetWithoutSourceFailsFast(t *testing.T) {
	gw := newGatewayWithBackend(t, DiscoveryScheme+":///backend")
	err := startUntilFailure(t, gw)
	if err == nil || !strings.Contains(err.Error(), "no discovery") {
		t.Fatalf("Start err = %v, want fail-fast 说明缺少发现来源", err)
	}
}

// WithDiscoverySource：装配期拿不到实例时按地址延迟取（段驱动契约层用这根线）。
func TestGatewayServerDiscoverySourceResolvesBackend(t *testing.T) {
	lis, srv := startGRPCServer(t)
	defer srv.Stop()

	d := inmemory.New()
	if err := d.Register(context.Background(), inst("b1", "backend", lis.Addr().String())); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	gw := newGatewayWithBackend(t, DiscoveryScheme+":///backend",
		WithDiscoverySource(func() (registry.Discovery, error) {
			calls.Add(1)
			return d, nil
		}))

	conn := serveGateway(t, gw)
	conn.Connect()
	if !waitReady(t, conn, 3*time.Second) {
		t.Fatalf("延迟来源解析出的端点没被拨通（state=%v）", conn.GetState())
	}
	if calls.Load() == 0 {
		t.Fatal("WithDiscoverySource 的闭包没被调用——Ready 来自别处")
	}
}

// 直连地址不得触发延迟来源：接线是按需的，不是每次拨号都去装配面上取一次。
func TestGatewayServerDirectAddrIgnoresDiscoverySource(t *testing.T) {
	var called atomic.Bool
	gw := newGatewayWithBackend(t, "127.0.0.1:19090",
		WithDiscoverySource(func() (registry.Discovery, error) {
			called.Store(true)
			return nil, errors.New("must not be called for a direct address")
		}))
	if conn := serveGateway(t, gw); conn == nil {
		t.Fatal("直连地址也该建立后端连接")
	}
	if called.Load() {
		t.Fatal("host:port 地址却调用了发现来源")
	}
}

// 显式实例优先于延迟来源（契约层把来源排在调用方 opts 之前，靠的是本判据
// 而不是选项顺序）。
func TestGatewayServerExplicitDiscoveryBeatsSource(t *testing.T) {
	lis, srv := startGRPCServer(t)
	defer srv.Stop()

	d := inmemory.New()
	if err := d.Register(context.Background(), inst("b1", "backend", lis.Addr().String())); err != nil {
		t.Fatal(err)
	}
	dead := inmemory.New()
	// 端口 1：必然拨不通，用来证明 Ready 来自显式实例而不是来源里的坏端点。
	if err := dead.Register(context.Background(), inst("b2", "backend", "127.0.0.1:1")); err != nil {
		t.Fatal(err)
	}
	var called atomic.Bool
	gw := newGatewayWithBackend(t, DiscoveryScheme+":///backend",
		WithDiscoverySource(func() (registry.Discovery, error) {
			called.Store(true)
			return dead, nil
		}),
		WithDiscovery(d))

	conn := serveGateway(t, gw)
	conn.Connect()
	if !waitReady(t, conn, 3*time.Second) {
		t.Fatalf("显式 Discovery 未生效（state=%v）", conn.GetState())
	}
	if called.Load() {
		t.Fatal("显式 Discovery 已在，仍去取了延迟来源")
	}
}

// 来源报错 → Start 失败并带上原始错误（拨不出连接的配置不该带着坏状态继续服务）。
func TestGatewayServerDiscoverySourceErrorFailsStart(t *testing.T) {
	wantErr := errors.New("registry offline")
	gw := newGatewayWithBackend(t, DiscoveryScheme+":///svc",
		WithDiscoverySource(func() (registry.Discovery, error) { return nil, wantErr }))
	if err := startUntilFailure(t, gw); !errors.Is(err, wantErr) {
		t.Fatalf("Start err = %v, want wrap of %v", err, wantErr)
	}
}

// 来源返回 (nil, nil) 同样要 fail-fast：这是对外闭包的边界，不能把 nil Discovery
// 交给 gRPC（那会退回默认 dns resolver，等于回到「静默拨一个不存在的主机名」）。
func TestGatewayServerDiscoverySourceNilFailsFast(t *testing.T) {
	gw := newGatewayWithBackend(t, DiscoveryScheme+":///svc",
		WithDiscoverySource(func() (registry.Discovery, error) { return nil, nil }))
	err := startUntilFailure(t, gw)
	if err == nil || !strings.Contains(err.Error(), "returned nil") {
		t.Fatalf("Start err = %v, want fail-fast 说明来源给了 nil", err)
	}
}

// newGatewayWithBackend 用测试用的 HTTP 面（127.0.0.1:0）与给定后端地址构造网关。
func newGatewayWithBackend(t *testing.T, backend string, opts ...Option) *GatewayServer {
	t.Helper()
	gw, err := NewGatewayServer(
		&bootstrapv1.Server_Http{Addr: "127.0.0.1:0"},
		&bootstrapv1.Server_Grpc{Addr: backend},
		func(context.Context, *grpc.ClientConn) (http.Handler, error) { return nil, nil },
		opts...,
	)
	if err != nil {
		t.Fatalf("NewGatewayServer: %v", err)
	}
	return gw
}

// serveGateway 后台 Start（期望成功），挂停机清理，返回建好的后端连接。
func serveGateway(t *testing.T, gw *GatewayServer) *grpc.ClientConn {
	t.Helper()
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
	return waitForBackendConn(t, gw)
}

// startUntilFailure 等 Start 返回错误；阻塞进服务即测试失败（带界避免拖到全局超时）。
func startUntilFailure(t *testing.T, gw *GatewayServer) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- gw.Start(ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		_ = gw.Stop(context.Background())
		<-done
		t.Fatal("Start 没有失败（已进入服务），want fail-fast")
		return nil
	}
}

// waitForBackendConn 等 Start 建好后端连接（同包内读私有字段，加锁避免竞态）。
func waitForBackendConn(t *testing.T, gw *GatewayServer) *grpc.ClientConn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		gw.mu.Lock()
		c := gw.conn
		gw.mu.Unlock()
		if c != nil {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Start 未建立后端连接")
	return nil
}

// UpdateState 失败是 watch-based resolver 唯一必须自己处理的错误路径：不重试
// 解析可以，但错误必须上报，否则「解析对了却推不进 ClientConn」是静默故障。
func TestPublishFailureIsReported(t *testing.T) {
	d := inmemory.New()
	if err := d.Register(context.Background(), inst("a1", "svc", "127.0.0.1:9001")); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(DiscoveryScheme + ":///svc")
	if err != nil {
		t.Fatal(err)
	}
	cc := &rejectingClientConn{fakeClientConn: newFakeClientConn()}
	r, err := newDiscoveryBuilder(d).Build(resolver.Target{URL: *u}, cc, resolver.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer r.Close()
	cc.waitFor(t, 1, time.Second)
	errs := cc.errList()
	if len(errs) == 0 {
		t.Fatal("UpdateState 被拒绝却没有任何上报")
	}
	if !strings.Contains(errs[0].Error(), "publish svc") {
		t.Fatalf("上报内容 = %v, want 含 publish svc", errs[0])
	}
}

// rejectingClientConn 让 UpdateState 恒失败，模拟 balancer 拒收状态。
type rejectingClientConn struct {
	*fakeClientConn
}

func (r *rejectingClientConn) UpdateState(resolver.State) error {
	r.mu.Lock()
	r.states = append(r.states, resolver.State{}) // 让 waitFor 仍能推进
	r.mu.Unlock()
	select {
	case r.updates <- struct{}{}:
	default:
	}
	return errors.New("bad resolver state")
}
