package appkit

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/kalandramo/bald/transport"
)

// TestPlan_ImperativeSlotsVisible 钉住 §6 的 Data/Imperative 分区落点：闭包
// 不参与排序，但 effect/keyWatch/reconcile/hook 声明必须在计划里可见（否则
// --plan 漏报配置热更订阅者、停机效果与生命周期钩子）。
func TestPlan_ImperativeSlotsVisible(t *testing.T) {
	p, err := Compile(context.Background(),
		ConfigFile(writePlanConfig(t, "imperative", "")),
		effectOpt("cleanup-db", func(context.Context) error { return nil }),
		onKeyChangeOpt("server.http", func(_, _ string) {}),
		reconcileOpt("logger", func(context.Context, *ReconcileCtx) error { return nil }),
		BeforeStart(func(context.Context) error { return nil }),
		AfterStop(func(context.Context) error { return nil }),
		WithExtraServerFunc(func(context.Context) (transport.Server, error) { return nil, nil }),
		OnConfigChange(func(map[string]any) {}),
	)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	want := []Imperative{
		{Kind: "effect", Name: "cleanup-db"},
		{Kind: "hook", Name: "afterStop[0]"},
		{Kind: "hook", Name: "beforeStart[0]"},
		{Kind: "keyWatch", Name: "server.http"},
		{Kind: "onConfigChange"},
		{Kind: "reconcile", Name: "logger"},
		{Kind: "serverFunc", Name: "extraServer[0]"},
	}
	if !reflect.DeepEqual(p.Imperative, want) {
		t.Fatalf("imperative = %+v, want %+v", p.Imperative, want)
	}

	var out strings.Builder
	p.Explain(&out)
	for _, line := range []string{
		"imperative:", "effect       cleanup-db", "hook         beforeStart[0]",
		"hook         afterStop[0]", "keyWatch     server.http",
		"onConfigChange -", "reconcile    logger", "serverFunc   extraServer[0]",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("plan output missing %q:\n%s", line, out.String())
		}
	}
}

// TestPlan_ImperativeOrderInvariant 声明序置换不改变可见化清单（排序键
// (Kind,Name)，与节点排序同源）。
func TestPlan_ImperativeOrderInvariant(t *testing.T) {
	cfgPath := writePlanConfig(t, "imperative-order", "")
	opts := []Option{
		effectOpt("b-effect", func(context.Context) error { return nil }),
		effectOpt("a-effect", func(context.Context) error { return nil }),
		reconcileOpt("z-reconcile", func(context.Context, *ReconcileCtx) error { return nil }),
	}

	first, err := Compile(context.Background(), append([]Option{ConfigFile(cfgPath)}, opts...)...)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	reversed := []Option{ConfigFile(cfgPath), opts[2], opts[1], opts[0]}
	second, err := Compile(context.Background(), reversed...)
	if err != nil {
		t.Fatalf("Compile (reordered): %v", err)
	}
	if !reflect.DeepEqual(first.Imperative, second.Imperative) {
		t.Fatalf("imperative list depends on declaration order: %+v vs %+v", first.Imperative, second.Imperative)
	}
	if first.Hash() != second.Hash() {
		t.Fatal("plan hash depends on declaration order")
	}
}
