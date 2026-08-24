package appkit

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	baldoptions "github.com/kalandramo/bald/pkg/options"
	"github.com/kalandramo/bald/pkg/registry"
	"github.com/kalandramo/bald/pkg/server"
)

// mockServer 是一个可控的测试服务器，满足 server.Server 接口。
type mockServer struct {
	addr         string
	started      atomic.Bool
	stopped      atomic.Bool
	startBlock   chan struct{} // 关闭后 Start 才返回 nil
	startErr     error         // 若非 nil，Start 立即返回该错误（模拟崩溃）
	stopDelay    time.Duration // 模拟 Stop 耗时
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

	var firstErr, secondErr error
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		firstErr = app.Run(ctx)
	}()
	go func() {
		time.Sleep(5 * time.Millisecond)
		secondErr = app.Run(ctx)
	}()

	// 等待第二个 Run 返回后，让第一个 Run 正常退出。
	for secondErr == nil {
		time.Sleep(2 * time.Millisecond)
	}
	if secondErr != ErrAlreadyRunning {
		t.Fatalf("expected ErrAlreadyRunning, got %v", secondErr)
	}
	cancel() // 让首个 Run 通过 ctx 取消退出（模拟停机信号）
	<-done1
	_ = firstErr
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
// Run 启动期把合并结果放入 a.Viper()，BeforeStart 可读到正确值。
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
			v := app.Viper()
			if v == nil {
				t.Fatal("Viper() is nil in BeforeStart")
			}
			loadedAddr = v.GetString("http.addr")
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
			loadedAddr = app.Viper().GetString("http.addr")
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
	httpSrv := server.NewHTTPServer(&baldoptions.HTTPOptions{Addr: ":0"}, http.NewServeMux())
	grpcSrv := server.NewGRPCServerWithRegister(&baldoptions.GRPCOptions{Addr: ":0"}, nil, nil)

	app := New(
		ID("node-multi"),
		Name("multi-svc"),
		Version("v1.2.3"),
		Registrar(reg),
		Servers(grpcSrv, httpSrv),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
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
