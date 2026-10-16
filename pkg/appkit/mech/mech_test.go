// mech_test.go 钉住批 2 的核心契约：mech 构造器返回**自持 option 类型**、
// 实现 appkit.Option（interface{ Apply(*appkit.Decl) }）、直写 Decl 导出
// 字段，并经 appkit.New 的 Decl 构造路径被引擎真实消费
// （装配层评估 §5.2 批 2 第一波、D1-target-appkit.md §5.2）。
package mech

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kalandramo/bald/pkg/appkit"
)

// 编译期断言：每个自持 option 类型都实现 appkit.Option（接口化后跨包
// 可实现——本波的存在性证明）。
var (
	_ appkit.Option = Effect("", nil)
	_ appkit.Option = EffectTimeout(0)
	_ appkit.Option = Provides()
	_ appkit.Option = Requires("", nil...)
	_ appkit.Option = Components()
	_ appkit.Option = ComponentTimeout(0)
	_ appkit.Option = OnKeyChange("", nil)
	_ appkit.Option = Reconcile("", nil)
)

// fakeComp 是引擎可见性验证用的最小组件。
type fakeComp struct {
	name     string
	disposed *bool
}

func (f fakeComp) Name() string                { return f.name }
func (f fakeComp) Start(context.Context) error { return nil }
func (f fakeComp) Dispose(context.Context) error {
	*f.disposed = true
	return nil
}

// TestMechOptionsReachEngine mech Option 经 New → Decl → newFromDecl 全链路
// 后，各机制的引擎消费面（Resolve 校验、效应账本回放、组件入列）可见。
func TestMechOptionsReachEngine(t *testing.T) {
	var undone []string
	disposed := false
	app := appkit.NewApp(
		Effect("m1", func(context.Context) error { undone = append(undone, "m1"); return nil }),
		EffectTimeout(3*time.Second),
		Provides("cap-a"),
		Requires("comp.x", "cap-a"),
		Components(fakeComp{name: "comp.x", disposed: &disposed}),
		ComponentTimeout(2*time.Second),
		OnKeyChange("http.addr", func(old, new string) {}),
		Reconcile("r1", func(context.Context, *appkit.ReconcileCtx) error { return nil }),
	)

	// S1：mech.Provides/Requires 写入的能力声明被 Resolve 校验消费。
	if err := app.Resolve(); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// C1（mech.Components → 引擎）：组件的 Start/Dispose 序由 appkit 包内
	// component_test.go 全覆盖（Run 路径），此处不重复断言。

	// T1：mech.Effect 登记的效应被账本回放（回放即清空，幂等）。
	app.UndoEffects(context.Background())
	if len(undone) != 1 || undone[0] != "m1" {
		t.Fatalf("undo replay = %v, want [m1]", undone)
	}
	app.UndoEffects(context.Background())
	if len(undone) != 1 {
		t.Fatalf("second replay must be no-op, got %v", undone)
	}
}

// TestMechRequiresFailFast mech.Requires 声明的缺失能力在 Resolve 聚合报错
// （缺失项与依赖方组件名可定位）。
func TestMechRequiresFailFast(t *testing.T) {
	app := appkit.NewApp(
		Requires("audit.store", "db", "cache"),
	)
	err := app.Resolve()
	if err == nil {
		t.Fatal("missing capabilities must fail Resolve")
	}
	for _, want := range []string{`capability "db" (required by audit.store)`, `capability "cache" (required by audit.store)`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Resolve error %q missing %q", err, want)
		}
	}
}
