package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
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
		{"GetService 失败", &stubDiscovery{getErr: sentinel}, "discovery svc"},
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
	gw, err := NewGatewayServer(
		&bootstrapv1.Server_Http{Addr: "127.0.0.1:0"},
		&bootstrapv1.Server_Grpc{Addr: DiscoveryScheme + ":///backend"},
		func(_ context.Context, conn *grpc.ClientConn) (http.Handler, error) {
			return nil, nil
		},
		WithDiscovery(d),
	)
	if err != nil {
		t.Fatalf("NewGatewayServer: %v", err)
	}
	startCtx, cancelStart := context.WithCancel(context.Background())
	defer cancelStart()
	serveDone := make(chan error, 1)
	go func() { serveDone <- gw.Start(startCtx) }()
	defer func() {
		_ = gw.Stop(context.Background())
		if err := <-serveDone; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Start: %v", err)
		}
	}()

	// Start 里 s.conn 的赋值在 register 返回之后，连接可见即证明转码面拿到了
	// 拨号句柄（nil 的 register 也走同一条赋值路径，不断言其内容）。
	conn := waitForBackendConn(t, gw)
	conn.Connect()
	if !waitReady(t, conn, 3*time.Second) {
		t.Fatalf("gateway 到后端的连接未 Ready（state=%v）", conn.GetState())
	}
}

// 未注入 Discovery 时，discovery:/// 目标不得静默生效：gRPC 会把未注册的 scheme
// 退回默认 dns resolver，结果是拨不通而不是拨到别处。断言「不会 Ready」。
func TestGatewayServerWithoutDiscoveryCannotResolveDiscoveryTarget(t *testing.T) {
	gw, err := NewGatewayServer(
		&bootstrapv1.Server_Http{Addr: "127.0.0.1:0"},
		&bootstrapv1.Server_Grpc{Addr: DiscoveryScheme + ":///backend"},
		nil,
	)
	if err != nil {
		t.Fatalf("NewGatewayServer: %v", err)
	}
	startCtx, cancelStart := context.WithCancel(context.Background())
	defer cancelStart()
	serveDone := make(chan error, 1)
	go func() { serveDone <- gw.Start(startCtx) }()
	defer func() {
		_ = gw.Stop(context.Background())
		<-serveDone
	}()

	conn := waitForBackendConn(t, gw)
	conn.Connect()
	if waitReady(t, conn, 500*time.Millisecond) {
		t.Fatal("未注入 Discovery 却 Ready 了——discovery:/// 目标被静默接受")
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
