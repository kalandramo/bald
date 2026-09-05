package appkit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/registry"
	grpcserver "github.com/kalandramo/bald/transport/grpc"
	httpserver "github.com/kalandramo/bald/transport/http"
)

// mockServer 是一个可控的测试服务器，满足 transport.Server 接口。
type mockServer struct {
	addr       string
	started    atomic.Bool
	stopped    atomic.Bool
	startBlock chan struct{} // 关闭后 Start 才返回 nil
	startErr   error         // 若非 nil，Start 立即返回该错误（模拟崩溃）
	stopDelay  time.Duration // 模拟 Stop 耗时
}

func newMock(addr string) *mockServer {
	return &mockServer{addr: addr, startBlock: make(chan struct{})}
}

func (m *mockServer) Start(ctx context.Context) error {
	m.started.Store(true)
	if m.startErr != nil {
		return m.startErr
	}
	select {
	case <-m.startBlock:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *mockServer) Stop(ctx context.Context) error {
	if m.stopDelay > 0 {
		select {
		case <-time.After(m.stopDelay):
		case <-ctx.Done():
		}
	}
	m.stopped.Store(true)
	return nil
}

func (m *mockServer) Endpoint() string { return "mock://" + m.addr }

// BUG-1: Stop 必须接收未取消的 ctx，stopTimeout 才生效。
// stopTimeout=50ms，server Stop 耗时 30ms，应当正常返回（未触发超时）。
func TestBug1_StopTimeoutRespected(t *testing.T) {
	srv := newMock("s1")
	srv.stopDelay = 30 * time.Millisecond

	app := New(
		Name("bug1"),
		StopTimeout(50*time.Millisecond),
		Servers(srv),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel() // 触发停机
	}()

	if err := app.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !srv.stopped.Load() {
		t.Fatal("expected server to be stopped")
	}
}

// BUG-3: 某 server Start 崩溃，其余 server 必须被级联停止。
func TestBug3_CrashCascadeStop(t *testing.T) {
	good := newMock("good")
	bad := newMock("bad")
	bad.startErr = errors.New("boom")

	app := New(
		Name("bug3"),
		Servers(good, bad),
	)

	err := app.Run(context.Background())
	if err == nil {
		t.Fatal("expected Run to return the crash error")
	}
	if !errors.Is(err, bad.startErr) {
		t.Fatalf("expected crash error, got %v", err)
	}
	// 等待级联停止完成
	time.Sleep(20 * time.Millisecond)
	if !good.stopped.Load() {
		t.Fatal("expected healthy server to be cascade-stopped after crash")
	}
}

// 防重入：重复调用 Run 应返回 ErrAlreadyRunning。
func TestRun_NoReentrant(t *testing.T) {
	srv := newMock("s")
	app := New(Name("reentrant"), Servers(srv))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 用 channel 传递结果：直接读写共享变量并与轮询并发会构成数据竞争
	// （go test -race 可复现），必须经 channel 建立 happens-before。
	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	go func() { done1 <- app.Run(ctx) }()
	go func() {
		time.Sleep(5 * time.Millisecond)
		done2 <- app.Run(ctx)
	}()

	// 等待第二个 Run 返回后，让第一个 Run 正常退出。
	if err := <-done2; err != ErrAlreadyRunning {
		t.Fatalf("expected ErrAlreadyRunning, got %v", err)
	}
	cancel() // 让首个 Run 通过 ctx 取消退出（模拟停机信号）
	_ = <-done1
}

// 可观察性：Run 结束后 Done() 应关闭。
func TestObservability_DoneAndErr(t *testing.T) {
	srv := newMock("obs")
	app := New(Name("obs"), Servers(srv))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	if err := app.Run(ctx); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	select {
	case <-app.Done():
	case <-time.After(time.Second):
		t.Fatal("Done() not closed after Run")
	}
	if app.Err() != nil {
		t.Fatalf("expected nil err, got %v", app.Err())
	}
}

// TestRun_ConfigLoadFailureAllowsRetry 验证配置加载失败可回收重入槽：
// 失败时归还 running 但不 close done，因此后续仍可重试 Run。
//
// 这是 loadConfig 移到防重入 CAS 之后时必须保住的语义：原实现把 loadConfig
// 放在 CAS 之前正是为了「配置失败不占用 running/done」，改顺序后需用
// 「显式归还 running + 不注册 defer close」来等价实现。
func TestRun_ConfigLoadFailureAllowsRetry(t *testing.T) {
	withArgs(t, []string{})

	// 用一个必然失败的 Bind 让 loadConfig 返回错误。
	a := New(Name("retry"), Bind("http", struct{}{}))

	if err := a.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded, want config load error")
	}

	// done 不应被关闭：本次 Run 从未真正进入运行态。
	select {
	case <-a.Done():
		t.Fatal("Done() closed after config load failure, should stay open")
	default:
	}

	// running 已归还，仍可再次尝试（此处仍会因同样的配置错误失败，但不应
	// 返回 ErrAlreadyRunning，否则说明重入槽泄漏）。
	secondErr := a.Run(context.Background())
	if secondErr == nil {
		t.Fatal("second Run succeeded, want config load error")
	}
	if errors.Is(secondErr, ErrAlreadyRunning) {
		t.Fatal("second Run returned ErrAlreadyRunning: running slot leaked after config failure")
	}
}

// TestRun_ConcurrentLoadConfigSafe 回归防护：并发 Run 不得并发执行 loadConfig。
//
// 修复前 loadConfig 位于防重入 CAS 之前，两个并发 Run 会同时执行它，
// 并发写 a.cfg.cfgFile（pflag 绑定了该字段的地址）与 a.cfg.v，
// go test -race 可稳定复现。
func TestRun_ConcurrentLoadConfigSafe(t *testing.T) {
	withArgs(t, []string{})

	srv := newMock("concurrent")
	a := New(Name("concurrent"), Servers(srv))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const total = 4
	results := make(chan error, total)
	for i := 0; i < total; i++ {
		go func() { results <- a.Run(ctx) }()
	}

	// 恰好一个 Run 成功进入运行态，其余立即返回 ErrAlreadyRunning。
	// 注意：成功的那个会一直阻塞在 mock 的 startBlock 上，
	// 所以先收 total-1 个 ErrAlreadyRunning，再取消 ctx 收尾。
	for i := 0; i < total-1; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, ErrAlreadyRunning) {
				t.Fatalf("Run #%d = %v, want ErrAlreadyRunning", i+1, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("Run #%d did not return in time", i+1)
		}
	}

	// 收尾：取消 ctx，让仍在运行态的那个 Run 退出。
	cancel()
	select {
	case err := <-results:
		if err != nil {
			t.Fatalf("the running Run returned %v after cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the running Run did not exit after cancel")
	}
}

// registry: 正常启动应 Register，停机应 Deregister。
func TestRegistry_RegisterDeregister(t *testing.T) {
	reg := &recordingRegistrar{}
	srv := newMock("reg1")

	app := New(
		ID("node-1"),
		Name("svc"),
		Version("v1"),
		Registrar(reg),
		Servers(srv),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	if err := app.Run(ctx); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	if !reg.registered {
		t.Fatal("expected Register to be called")
	}
	if !reg.deregistered {
		t.Fatal("expected Deregister to be called")
	}
	if len(reg.instance.Endpoints) != 1 || reg.instance.Endpoints[0] != "mock://reg1" {
		t.Fatalf("unexpected endpoints: %v", reg.instance.Endpoints)
	}
	if reg.instance.ID != "node-1" || reg.instance.Name != "svc" {
		t.Fatalf("unexpected instance meta: %+v", reg.instance)
	}
}

// P0：分阶段停机顺序与独立超时。
// 验证顺序为 beforeStop -> transport.Stop -> afterStop，且各阶段用各自独立超时 ctx。
func TestP0_StopPhasesOrderAndTimeout(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
	}

	srv := newMock("phased")
	srv.stopDelay = 5 * time.Millisecond

	app := New(
		Name("phased"),
		StopTimeout(50*time.Millisecond),
		BeforeStopTimeout(20*time.Millisecond),
		AfterStopTimeout(20*time.Millisecond),
		Servers(srv),
		BeforeStop(func(ctx context.Context) error {
			record("beforeStop")
			// 阶段1 应拿到的 ctx 有自己的 deadline（独立超时）。
			if _, ok := ctx.Deadline(); !ok {
				t.Error("beforeStop ctx has no deadline")
			}
			return nil
		}),
		AfterStop(func(ctx context.Context) error {
			record("afterStop")
			if _, ok := ctx.Deadline(); !ok {
				t.Error("afterStop ctx has no deadline")
			}
			return nil
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	if err := app.Run(ctx); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	want := []string{"beforeStop", "afterStop"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("stop phase order = %v, want %v", got, want)
	}
	if !srv.stopped.Load() {
		t.Fatal("expected server stopped between phases")
	}
}

// P0：生命周期钩子 panic 不应拖垮整机停机（recover 隔离）。
func TestP0_HookPanicIsolated(t *testing.T) {
	srv := newMock("panichook")
	srv.stopDelay = 2 * time.Millisecond

	app := New(
		Name("panichook"),
		Servers(srv),
		BeforeStop(func(ctx context.Context) error {
			panic("boom in beforeStop")
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	// Run 应正常返回（panic 被 recover 隔离），且服务器仍被停止。
	if err := app.Run(ctx); err != nil {
		t.Fatalf("Run should not fail on hook panic, got: %v", err)
	}
	if !srv.stopped.Load() {
		t.Fatal("expected server stopped despite hook panic")
	}
}

// P1：泛型注册表 + 重名防护 + 并发安全。
func TestP1_Registry(t *testing.T) {
	r := NewRegistry[string]()
	if err := r.Register("a", "1"); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if err := r.Register("a", "x"); err == nil {
		t.Fatal("expected duplicate register error")
	}
	if err := r.Register("b", "2"); err != nil {
		t.Fatalf("register b: %v", err)
	}
	if v, ok := r.Get("a"); !ok || v != "1" {
		t.Fatalf("get a = %q,%v", v, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("get missing should be false")
	}
	if got := r.List(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("list = %v, want [a b]", got)
	}

	// 并发注册不 panic / 不数据竞争。
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = r.Register(fmt.Sprintf("c%d", i), "v")
		}(i)
	}
	wg.Wait()
	if len(r.List()) != 52 {
		t.Fatalf("after concurrent register list len = %d, want 52", len(r.List()))
	}
}

// P1：存储 Provider 注册点可被桥接子模块经 init 自注册后按名获取。
func TestP1_StoreProviderRegistry(t *testing.T) {
	// 模拟 bald-store-gorm/register 的 init() 自注册。
	RegisterStoreProvider("gorm", func() string { return "gorm-provider" })

	factory, ok := ProviderRegistry.Get("gorm")
	if !ok {
		t.Fatal("gorm provider not registered")
	}
	fn, ok := factory.(func() string)
	if !ok {
		t.Fatalf("factory type = %T, want func() string", factory)
	}
	if fn() != "gorm-provider" {
		t.Fatal("unexpected factory result")
	}
	if names := ProviderRegistry.List(); len(names) != 1 || names[0] != "gorm" {
		t.Fatalf("provider list = %v, want [gorm]", names)
	}
}

// recordingRegistrar 记录注册/反注册调用。
type recordingRegistrar struct {
	mu           sync.Mutex
	registered   bool
	deregistered bool
	instance     *registry.ServiceInstance
}

func (r *recordingRegistrar) Register(_ context.Context, in *registry.ServiceInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registered = true
	r.instance = in
	return nil
}

func (r *recordingRegistrar) Deregister(_ context.Context, _ *registry.ServiceInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deregistered = true
	return nil
}

// --- 配置集成（单元测试级，内存源，无外部依赖） ---

// memRemoteSource 内存实现 config.RemoteSource，用于 appkit 端到端测试。
type memRemoteSource struct {
	data   []byte
	format string
}

func (s *memRemoteSource) Read(_ context.Context) ([]byte, string, error) {
	return s.data, s.format, nil
}
func (s *memRemoteSource) Watch(_ context.Context, _ func([]byte, string)) error {
	return nil
}

// TestAppKit_ConfigLoadedAndExposed：WithConfig + RemoteConfig 注入后，
// Run 启动期把合并结果放入 a.Settings()/Config()，BeforeStart 可读到正确值。
func TestAppKit_ConfigLoadedAndExposed(t *testing.T) {
	// 远程基准：http.addr=:8080
	remote := &memRemoteSource{data: []byte("http:\n  addr: \":8080\""), format: "yaml"}
	// 本地覆盖：http.addr=:9090
	dir := t.TempDir()
	localPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(localPath, []byte("http:\n  addr: \":9090\""), 0o644); err != nil {
		t.Fatalf("write local: %v", err)
	}

	srv := newMock("cfg")
	var loadedAddr string
	var app *AppKit
	app = New(
		Name("cfg-demo"),
		ConfigFile(localPath),
		RemoteConfig(remote),
		Servers(srv),
		BeforeStart(func(_ context.Context) error {
			m := app.Settings()
			if m == nil {
				t.Fatal("Settings() is nil in BeforeStart")
			}
			loadedAddr = app.Config().GetString("http.addr")
			return nil
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	if err := app.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 本地应覆盖远程基准。
	if loadedAddr != ":9090" {
		t.Fatalf("BeforeStart saw http.addr=%q, want :9090", loadedAddr)
	}
}

// TestAppKit_ConfigMissingFileNoError：本地文件缺失不报错（onexstack 行为），
// 远程基准生效。
func TestAppKit_ConfigMissingFileNoError(t *testing.T) {
	remote := &memRemoteSource{data: []byte("http:\n  addr: \":7070\""), format: "yaml"}
	srv := newMock("cfg2")
	var loadedAddr string
	var app *AppKit
	app = New(
		Name("cfg-demo"),
		// 不传 ConfigFile：纯远程/纯 flag 配置，本地文件缺失不应报错（onexstack 风格）。
		RemoteConfig(remote),
		Servers(srv),
		BeforeStart(func(_ context.Context) error {
			loadedAddr = app.Config().GetString("http.addr")
			return nil
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	if err := app.Run(ctx); err != nil {
		t.Fatalf("Run should not fail without local file: %v", err)
	}
	if loadedAddr != ":7070" {
		t.Fatalf("http.addr=%q, want :7070 (remote baseline)", loadedAddr)
	}
}

// --- 多 server Endpoint 聚合（动态端口 :0）注册 ---

// realHTTPServer / realGRPCServer 包装真实 server，供 appkit 端到端编排测试，
// 验证 buildInstance 正确聚合多个 :0 动态端口后的 Endpoint。
func TestAppKit_MultiServerEndpointAggregation(t *testing.T) {
	reg := &recordingRegistrar{}
	httpSrv := httpserver.NewHTTPServer(&bootstrapv1.Server_Http{Addr: ":0"}, http.NewServeMux(), nil)
	grpcSrv := grpcserver.NewGRPCServerWithRegister(&bootstrapv1.Server_Grpc{Addr: ":0"}, nil, nil, nil)

	app := New(
		ID("node-multi"),
		Name("multi-svc"),
		Version("v1.2.3"),
		Registrar(reg),
		Servers(grpcSrv, httpSrv),
	)

	ctx, cancel := context.WithCancel(context.Background())
	// 等待 server 真正监听并注册完成后再取消，验证优雅退出路径。
	// 取消过早会撞在 waitForEndpoints 阶段，导致 Run 返回 context 错误。
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	if err := app.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 注册实例应含两个 Endpoint（grpc + http），均为真实解析端口，非 :0。
	inst := reg.instance
	if inst == nil {
		t.Fatal("instance not recorded")
	}
	if len(inst.Endpoints) != 2 {
		t.Fatalf("endpoints len = %d, want 2 (grpc + http)", len(inst.Endpoints))
	}
	for _, ep := range inst.Endpoints {
		if strings.HasSuffix(ep, ":0") {
			t.Fatalf("endpoint still :0, dynamic port not resolved: %q", ep)
		}
		// 注册地址必须对调用方可达：host 不能是通配符 / 空 / 环回地址。
		// 此前直接返回 ln.Addr()（=0.0.0.0:port），其他节点无法直连——此断言固化修复。
		u, err := url.Parse(ep)
		if err != nil {
			t.Fatalf("parse endpoint %q: %v", ep, err)
		}
		host := u.Hostname()
		ip := net.ParseIP(host)
		if ip == nil {
			t.Fatalf("endpoint %q host %q is not an IP (unreachable wildcard?)", ep, host)
		}
		if ip.IsUnspecified() || ip.IsLoopback() {
			t.Fatalf("endpoint %q host %q is unspecified/loopback, not reachable by peers", ep, host)
		}
	}
	// 多 server 时 Kind 应为 mixed。
	if inst.Kind != "mixed" {
		t.Fatalf("kind = %q, want mixed", inst.Kind)
	}
	if inst.Metadata["scheme"] != "mixed" {
		t.Fatalf("metadata scheme = %q, want mixed", inst.Metadata["scheme"])
	}
	if inst.Version != "v1.2.3" || inst.ID != "node-multi" || inst.Name != "multi-svc" {
		t.Fatalf("unexpected instance meta: %+v", inst)
	}
}
