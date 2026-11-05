package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确、Option 嵌入面可用（Apply 追加进声明）、Plan 只有一个
//      与段路径同名的节点；
//   2. 段在 → Build 产物落 map[string]any{"datadog": *}，键名与资源面路径
//      （instrumentation.datadog）同形，cleanup 非 nil（statsd 有连接要关）；
//   3. 子段缺失 → 复用 Provider 的 fail-fast（不产半成品实例）。
//
// 构造只建 UDP socket，不需要 agent；报文级判据在 mapping_offline_test.go。

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/metrics/datadog"
	"github.com/kalandramo/bald/pkg/appkit"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "instrumentation.datadog" {
		t.Fatalf("Segments() = %v, want [instrumentation.datadog]", segs)
	}
}

func TestAppProvider_ApplyAddsToDecl(t *testing.T) {
	d := &appkit.Decl{}
	AppProvider().Apply(d)
	if len(d.Providers) != 1 {
		t.Fatalf("decl providers = %d, want 1", len(d.Providers))
	}
}

func TestAppProvider_PlanMatchesSegmentPath(t *testing.T) {
	nodes, err := appProvider{}.Plan(context.Background(), appkit.PlanScope{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("Plan 节点数 = %d, want 1", len(nodes))
	}
	n := nodes[0]
	if n.Key != appkit.ResourceKey("instrumentation.datadog") || len(n.Segments) != 1 || n.Segments[0] != string(n.Key) {
		t.Fatalf("Plan 节点 = %+v, want Key==Segments[0]==instrumentation.datadog", n)
	}
	if n.Priority != appkit.PriorityMetrics {
		t.Fatalf("Priority = %d, want PriorityMetrics(%d)", n.Priority, appkit.PriorityMetrics)
	}
	if n.EffectName != "appkit:instrumentation-datadog" {
		t.Fatalf("EffectName = %q, want appkit:instrumentation-datadog", n.EffectName)
	}
}

func TestAppProvider_BuildProductLandsOnKeyPath(t *testing.T) {
	scope := appkit.ApplyScope{Config: &bootstrapv1.BootstrapConfig{
		Instrumentation: &bootstrapv1.Instrumentation{
			Datadog: &bootstrapv1.Instrumentation_Datadog{Address: "127.0.0.1:18125"},
		},
	}}
	product, cleanup, err := appProvider{}.Build(context.Background(), scope, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup = nil, want non-nil（DogStatsD 客户端要关）")
	}
	m, ok := product.(map[string]any)
	if !ok {
		t.Fatalf("product type = %T, want map[string]any", product)
	}
	p, ok := m["datadog"].(*datadog.Provider)
	if !ok {
		t.Fatalf("product[datadog] type = %T, want *datadog.Provider", m["datadog"])
	}
	// 产物键 + 顶层段名拼出的资源路径必须与导出句柄的 Key 同形——不一致的话
	// 消费侧 Datadog.Resource(app) 永远取不到，而且不报错。
	if got := "instrumentation.datadog"; got != string(Datadog.Name()) {
		t.Fatalf("资源路径 %q 与句柄名 %q 不一致", got, Datadog.Name())
	}
	if p == nil {
		t.Fatal("产物是 nil 指针")
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

func TestAppProvider_SubSectionMissingFailsFast(t *testing.T) {
	scope := appkit.ApplyScope{Config: &bootstrapv1.BootstrapConfig{
		Instrumentation: &bootstrapv1.Instrumentation{},
	}}
	_, _, err := appProvider{}.Build(context.Background(), scope, appkit.Node{})
	if err == nil {
		t.Fatal("子段缺失应 fail-fast")
	}
}
