package appkit

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// TestPlan_ImperativeSlotsVisible 钉住 §6 的 Data/Imperative 分区落点：闭包
// 不参与排序，但 effect/keyWatch/reconcile 声明必须在计划里可见（否则 --plan
// 漏报配置热更订阅者与停机效果）。
func TestPlan_ImperativeSlotsVisible(t *testing.T) {
	p, err := Compile(context.Background(),
		ConfigFile(writePlanConfig(t, "imperative", "")),
		effectOpt("cleanup-db", func(context.Context) error { return nil }),
		onKeyChangeOpt("server.http", func(_, _ string) {}),
		reconcileOpt("logger", func(context.Context, *ReconcileCtx) error { return nil }),
	)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	want := []Imperative{
		{Kind: "effect", Name: "cleanup-db"},
		{Kind: "keyWatch", Name: "server.http"},
		{Kind: "reconcile", Name: "logger"},
	}
	if !reflect.DeepEqual(p.Imperative, want) {
		t.Fatalf("imperative = %+v, want %+v", p.Imperative, want)
	}

	var out strings.Builder
	p.Explain(&out)
	for _, line := range []string{"imperative:", "effect       cleanup-db", "keyWatch     server.http", "reconcile    logger"} {
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
