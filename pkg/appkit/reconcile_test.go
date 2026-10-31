package appkit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/kalandramo/bald/pkg/audit"
)

// ---- 测试桩：期望态来源与可观测组件 ----

// reconSrv 是满足 transport.Server 的最小桩（Endpoint 返回确定地址以过 waitForEndpoints）。
type reconSrv struct{}

func (reconSrv) Start(context.Context) error { return nil }
func (reconSrv) Stop(context.Context) error  { return nil }
func (reconSrv) Endpoint() string            { return "http://127.0.0.1:18098" }

// reconComp 可断言启停的组件。
type reconComp struct {
	name     string
	log      *compLog
	startErr error

	mu       sync.Mutex // 保护 started/disposed：协调 goroutine 写、测试断言读
	started  bool
	disposed bool
}

func (c *reconComp) Name() string { return c.name }
func (c *reconComp) Start(context.Context) error {
	if c.startErr != nil {
		c.log.add("start-fail:" + c.name)
		return c.startErr
	}
	c.mu.Lock()
	c.started = true
	c.mu.Unlock()
	c.log.add("start:" + c.name)
	return nil
}
func (c *reconComp) Dispose(context.Context) error {
	c.mu.Lock()
	c.disposed = true
	c.mu.Unlock()
	c.log.add("dispose:" + c.name)
	return nil
}

// state 读取启停状态（线程安全，供断言使用）。
func (c *reconComp) state() (started, disposed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started, c.disposed
}

// newReconSettings 构造期望态配置快照（audit.backends = 逗号分隔后端列表）。
func newReconSettings(want string) map[string]any {
	return map[string]any{"audit": map[string]any{"backends": want}}
}

// diffStrings 是 mech.DiffStrings 的测试内联副本（仅用于本文件期望值计算）：
// DiffStrings 导出面归 mech，而 in-package 测试不可 import mech
// （appkit→mech→appkit 导入环）。
func diffStrings(want, have []string) (add, remove []string) {
	wantSet := make(map[string]struct{}, len(want))
	haveSet := make(map[string]struct{}, len(have))
	for _, w := range want {
		wantSet[w] = struct{}{}
	}
	for _, h := range have {
		haveSet[h] = struct{}{}
	}
	for w := range wantSet {
		if _, ok := haveSet[w]; !ok {
			add = append(add, w)
		}
	}
	for h := range haveSet {
		if _, ok := wantSet[h]; !ok {
			remove = append(remove, h)
		}
	}
	sort.Strings(add)
	sort.Strings(remove)
	return add, remove
}

// convergeFn 是通用协调逻辑：期望列表 vs 已挂载列表，增/删到收敛。
// 工厂按名构造组件（failOn 指定哪个名字构造出的组件 Start 必然失败）。
func convergeFn(failOn string, comps *map[string]*reconComp) ReconcileFunc {
	return func(ctx context.Context, r *ReconcileCtx) error {
		want := r.StringSlice("audit.backends")
		add, remove := diffStrings(want, r.Mounted())
		for _, name := range add {
			c := &reconComp{name: name, log: &compLog{}}
			if name == failOn {
				c.startErr = errors.New("backend unavailable")
			}
			(*comps)[name] = c
			if err := r.Mount(ctx, name, c); err != nil {
				delete(*comps, name)
				return err // 失败不回滚，下次协调继续收敛
			}
		}
		for _, name := range remove {
			if err := r.Unmount(ctx, name); err != nil {
				return err
			}
			delete(*comps, name)
		}
		return nil
	}
}

// setupReconApp 构造运行中的 App + 协调器（原语要求 Run 存活期）。
func setupReconApp(t *testing.T, failOn string) (*App, *map[string]*reconComp, func()) {
	t.Helper()
	comps := &map[string]*reconComp{}
	app := NewApp(
		Name("recon-test"),
		Servers(reconSrv{}),
		reconcileOpt("audit.backends", convergeFn(failOn, comps)),
		AfterStart(func(ctx context.Context) error { return nil }),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = app.Run(ctx) }()
	cleanup := func() {
		cancel()
		<-done // 先 cancel 再等（LIFO 死锁坑：不可拆成两个 t.Cleanup）
	}
	t.Cleanup(cleanup)
	// 等 Run 进入存活期（MountComponent 要求 running=true）；不预跑协调，
	// 由各测试自行设置期望态后触发，避免空配置协调干扰断言。
	for i := 0; i < 1000 && !app.running.Load(); i++ {
		time.Sleep(time.Millisecond)
	}
	return app, comps, cleanup
}

// TestReconcile_ConvergesToDesiredState 核心契约：期望态 [log,store,stream] →
// 三个组件全部挂载并 Start。
func TestReconcile_ConvergesToDesiredState(t *testing.T) {
	app, comps, _ := setupReconApp(t, "")
	app.runReconcilersWith(context.Background(), newReconSettings("log,store,stream"))

	mounted := app.reconMounted("audit.backends")
	if len(mounted) != 3 {
		t.Fatalf("mounted = %v, want 3 [log store stream]", mounted)
	}
	for _, name := range []string{"log", "store", "stream"} {
		c, ok := (*comps)[name]
		if !ok || !c.started {
			t.Fatalf("component %s should be started", name)
		}
	}
}

// TestReconcile_Idempotent 幂等契约：重复协调不产生重复挂载/重复启停。
func TestReconcile_Idempotent(t *testing.T) {
	app, comps, _ := setupReconApp(t, "")
	want := newReconSettings("log,store")
	app.runReconcilersWith(context.Background(), want)
	app.runReconcilersWith(context.Background(), want) // 二次协调应为 no-op
	app.runReconcilersWith(context.Background(), want)

	if got := app.reconMounted("audit.backends"); len(got) != 2 {
		t.Fatalf("after 3 reconciles mounted = %v, want 2", got)
	}
	c, ok := (*comps)["log"]
	if !ok {
		t.Fatal("log should be mounted")
	}
	if started, disposed := c.state(); !started || disposed {
		t.Fatalf("log state = (started=%v disposed=%v), want (true,false)", started, disposed)
	}
}

// TestReconcile_RemovesExtra 移除契约：期望态缩小 → 多余组件被卸载并 Dispose。
func TestReconcile_RemovesExtra(t *testing.T) {
	app, comps, _ := setupReconApp(t, "")
	app.runReconcilersWith(context.Background(), newReconSettings("log,store,stream"))
	stream := (*comps)["stream"]

	app.runReconcilersWith(context.Background(), newReconSettings("log")) // 期望态缩小
	app.runReconcilersWith(context.Background(), newReconSettings("log")) // 重复协调应幂等

	if got := app.reconMounted("audit.backends"); len(got) != 1 || got[0] != "log" {
		t.Fatalf("after shrink mounted = %v, want [log]", got)
	}
	if !stream.disposed {
		t.Fatal("removed component should be disposed")
	}
}

// TestReconcile_PartialFailureThenConverges 收敛性核心契约：某组件挂载失败导致
// 中间态，故障消失后再次协调能继续补齐（不要求单次成功）。
func TestReconcile_PartialFailureThenConverges(t *testing.T) {
	app, comps, _ := setupReconApp(t, "stream") // stream 构造即失败
	desired := newReconSettings("log,store,stream")

	// 第一次：log/store 成功，stream 失败（停在中间态）。
	app.runReconcilersWith(context.Background(), desired)
	if got := app.reconMounted("audit.backends"); len(got) != 2 {
		t.Fatalf("partial: mounted = %v, want [log store]", got)
	}

	// 故障消失（换成不失败的工厂）→ 再次协调补齐 stream。
	// 注意：替换协调器须在锁内改、锁外跑协调（持锁调协调会自锁）。
	app.reconMu.Lock()
	app.reconcilers = []reconciler{{name: "audit.backends", fn: convergeFn("", comps)}}
	app.reconMu.Unlock()
	app.runReconcilersWith(context.Background(), desired)

	if got := app.reconMounted("audit.backends"); len(got) != 3 {
		t.Fatalf("after recovery mounted = %v, want 3", got)
	}
	if c := (*comps)["stream"]; c == nil || !c.started {
		t.Fatal("stream should converge after recovery")
	}
}

// TestReconcile_StartupAppliesInitialConfig 启动后首次协调：初始配置（真实 yaml 文件）
// 的期望态在 afterStart 之后即生效，无需等待配置变更事件。
func TestReconcile_StartupAppliesInitialConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(cfgPath, []byte("audit:\n  backends: log,store\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	comps := &map[string]*reconComp{}
	app := NewApp(
		Name("recon-init"),
		Servers(reconSrv{}),
		ConfigFile(cfgPath),
		reconcileOpt("audit.backends", convergeFn("", comps)),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = app.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	// 等启动后首次协调生效（afterStart 之后自动跑一次）。
	for i := 0; i < 1000; i++ {
		if len(app.reconMounted("audit.backends")) == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := app.reconMounted("audit.backends"); len(got) != 2 {
		t.Fatalf("initial reconcile mounted = %v, want [log store]", got)
	}
}

// reconAuditMem 是带锁的审计后端（Run 的协调 goroutine 会并发写事件）。
type reconAuditMem struct {
	mu     sync.Mutex
	events []audit.AuditEvent
}

func (m *reconAuditMem) Record(_ context.Context, ev audit.AuditEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, ev)
}

func (m *reconAuditMem) snapshot() []audit.AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]audit.AuditEvent(nil), m.events...)
}

// TestReconcile_AuditEvent 协调产生审计事件（Object="reconciler"）。
// 无全局审计器——sink 经显式注入（此处直设 App.auditor，等价 auditorOpt）。
func TestReconcile_AuditEvent(t *testing.T) {
	am := &reconAuditMem{}

	app, _, _ := setupReconApp(t, "")
	app.setAuditor(am, false)
	app.runReconcilersWith(context.Background(), newReconSettings("log"))

	found := false
	for _, ev := range am.snapshot() {
		if ev.Object == "reconciler" && ev.Action == "reconcile" {
			found = true
			if ev.Result != audit.ResultAllow {
				t.Errorf("reconcile result = %v, want allow", ev.Result)
			}
			if ev.Meta["reconciler"] != "audit.backends" {
				t.Errorf("meta reconciler = %v", ev.Meta["reconciler"])
			}
		}
	}
	if !found {
		t.Fatal("reconcile should emit audit event")
	}
}

// TestReconcileCtx_ValueSemantics 配置值语义（config 委托）：
// String 点路径标量化、StringSlice 列表解析。实现体在 bootstrap/config
// （config.At/String/StringList），此处钉住委托后的语义——与 config.StringList
// 完全一致（含收敛语义：[]any 非字符串项按标量化保留）。
func TestReconcileCtx_ValueSemantics(t *testing.T) {
	r := NewReconcileCtx(nil, "probe", map[string]any{
		"audit": map[string]any{"backends": "log, store ,stream"},
		"str":   map[string]any{"v": "x"},
		"num":   42,
		"list":  []any{"a", 1, "b"},
	})
	if got := r.String("str.v"); got != "x" {
		t.Errorf("nested string = %q, want x", got)
	}
	if got := r.String("num"); got != "42" {
		t.Errorf("scalar string = %q, want 42", got)
	}
	if got := r.String("missing"); got != "" {
		t.Errorf("missing = %q, want empty", got)
	}
	got := r.StringSlice("audit.backends")
	if len(got) != 3 || got[0] != "log" || got[1] != "store" || got[2] != "stream" {
		t.Errorf("comma list = %v, want [log store stream]", got)
	}
	lst := r.StringSlice("list")
	if len(lst) != 3 || lst[0] != "a" || lst[1] != "1" || lst[2] != "b" {
		t.Errorf("[]any list = %v, want [a 1 b] (config.StringList converged semantics)", lst)
	}
	if got := r.StringSlice("missing"); got != nil {
		t.Errorf("missing slice = %v, want nil", got)
	}
}
