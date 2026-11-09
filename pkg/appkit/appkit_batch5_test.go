package appkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/bald/health"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// b5Comp 是批5 测试用的最小可挂载组件（Name/Start/Dispose）。
type b5Comp struct{ name string }

func (c b5Comp) Name() string                  { return c.name }
func (c b5Comp) Start(context.Context) error   { return nil }
func (c b5Comp) Dispose(context.Context) error { return nil }

func TestHasHTTPFaceExported(t *testing.T) {
	if (&Decl{}).HasHTTPFace() {
		t.Error("空声明不该有 HTTP 面")
	}
	if !(&Decl{HTTPHandler: http.NotFoundHandler()}).HasHTTPFace() {
		t.Error("HTTPHandler 非空应有 HTTP 面")
	}
	var contract HTTPContractFunc = func(*bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
		return nil, nil, nil
	}
	if !(&Decl{HTTPContract: contract}).HasHTTPFace() {
		t.Error("HTTPContract 非空应有 HTTP 面（契约形态计划期无 handler 也算面）")
	}
}

func TestAddEffectRuntime(t *testing.T) {
	a := NewHarness()
	var undone bool
	a.addEffect("e1", func(context.Context) error { undone = true; return nil })
	if len(a.effects) != 1 {
		t.Fatalf("addEffect 应入账一条: got %d", len(a.effects))
	}
	a.UndoEffects(context.Background())
	if !undone {
		t.Error("已登记效应应在 UndoEffects 时回放")
	}
}

func TestWithProbesExported(t *testing.T) {
	business := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	// live/ready 传空串 → 走默认路径 /healthz、/readyz。
	h := WithProbes(business, health.New(), "", "")
	if h == nil {
		t.Fatal("WithProbes 应返回非 nil handler")
	}
	rec := &statusRecorder{code: http.StatusOK}
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.code != http.StatusOK {
		t.Errorf("默认存活路径应 200, got %d", rec.code)
	}
	// 非探针路径应转发给业务 handler。
	rec = &statusRecorder{code: http.StatusOK}
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/business", nil))
	if rec.code != http.StatusTeapot {
		t.Errorf("业务路径应落到 business handler (418), got %d", rec.code)
	}
}

func TestWithPlanCacheOption(t *testing.T) {
	d := newDecl(WithPlanCache(".bald/plan-cache.json"))
	if d.PlanCache != ".bald/plan-cache.json" {
		t.Errorf("WithPlanCache 未落到 Decl.PlanCache: %q", d.PlanCache)
	}
}

func TestRecordPlanCacheBranches(t *testing.T) {
	ctx := context.Background()

	// 分支1：空路径早退（缺省不落盘）。
	a := NewApp()
	a.recordPlanCache(ctx, "")

	// 分支2：路径非空但计划为 nil 早退。
	a.recordPlanCache(ctx, filepath.Join(t.TempDir(), "cache.json"))

	// 分支3：有效计划 → 写成功（含自动建目录）。
	a.plan = &Plan{Role: "api", InputHash: "h1"}
	good := filepath.Join(t.TempDir(), "sub", "cache.json")
	a.recordPlanCache(ctx, good)
	if _, err := os.Stat(good); err != nil {
		t.Fatalf("有效计划应写入基线: %v", err)
	}

	// 分支4：写失败（父路径是普通文件 → MkdirAll 得 ENOTDIR）→ 降级 debug。
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.recordPlanCache(ctx, filepath.Join(blocker, "cache.json"))
}

func TestReconcileExportedWrappers(t *testing.T) {
	ctx := context.Background()
	a := NewHarness()
	r := NewReconcileCtx(a, "audit.backends", nil)

	if r.Has("x") {
		t.Error("初始名下不该 Has x")
	}
	if err := r.Mount(ctx, "x", b5Comp{name: "x"}); err != nil {
		t.Fatalf("Mount 失败: %v", err)
	}
	if !r.Has("x") { // 覆盖 reconGet 命中分支
		t.Error("Mount 后应 Has x")
	}
	if r.Has("missing") { // 覆盖 reconGet 未命中分支
		t.Error("未挂载的组件不该 Has")
	}
	if len(r.Mounted()) != 1 {
		t.Errorf("Mounted 计数错误: %v", r.Mounted())
	}

	// ReconcileNow 走 runReconcilers（harness 无配置仓库 → settings 快照为 nil）。
	a.reconcilers = []reconciler{{name: "audit.backends", fn: func(context.Context, *ReconcileCtx) error {
		return nil
	}}}
	a.ReconcileNow(ctx)
}

// statusRecorder 是最小的 ResponseWriter，只关心状态码。
type statusRecorder struct{ code int }

func (s *statusRecorder) Header() http.Header         { return http.Header{} }
func (s *statusRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (s *statusRecorder) WriteHeader(c int)           { s.code = c }

func TestRollbackBuildsDirect(t *testing.T) {
	ctx := context.Background()
	var order []int
	mk := func(i int) func(context.Context) error {
		return func(context.Context) error { order = append(order, i); return nil }
	}
	// 逆序兑现已成功项的 cleanup：nil 跳过，非 nil 执行。
	rollbackBuilds([]buildResult{{cleanup: mk(0)}, {cleanup: nil}, {cleanup: mk(2)}}, ctx)
	if len(order) != 2 || order[0] != 2 || order[1] != 0 {
		t.Fatalf("应按逆序兑现非 nil cleanup: %v", order)
	}
}

func TestDoctorErrorWithoutConfig(t *testing.T) {
	// 无有效配置 → preloadContract 报 app.name required → Doctor 走 err 分支。
	if err := Doctor(context.Background()); err == nil {
		t.Error("无有效配置时 Doctor 应返回错误")
	}
}

func TestDoctorSuccessWithConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(cfgPath, []byte("app:\n  name: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Doctor(context.Background(), ConfigFile(cfgPath)); err != nil {
		t.Fatalf("有效配置下 Doctor 应成功输出诊断: %v", err)
	}
}
