package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确、Option 嵌入面可用（Apply 追加进声明）、Plan 只有一个
//      与段路径同名的节点；
//   2. 段在 → Build 产物落 map[string]any{"prometheus": *}，键名与资源面
//      路径（instrumentation.prometheus）一致；
//   3. 子段缺失 → 复用 Provider 的 fail-fast（不产半成品实例）。
//
// 埋点落在内存 Registry 上，不需要任何真后端，故本文件不引 fake server。

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	prom "github.com/kalandramo/bald/metrics/prometheus"
	"github.com/kalandramo/bald/pkg/appkit"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "instrumentation.prometheus" {
		t.Fatalf("Segments() = %v, want [instrumentation.prometheus]", segs)
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
	if n.Key != appkit.ResourceKey("instrumentation.prometheus") || len(n.Segments) != 1 || n.Segments[0] != string(n.Key) {
		t.Fatalf("Plan 节点 = %+v, want Key==Segments[0]==instrumentation.prometheus", n)
	}
	if n.Priority != appkit.PriorityMetrics {
		t.Fatalf("Priority = %d, want PriorityMetrics(%d)", n.Priority, appkit.PriorityMetrics)
	}
}

func TestAppProvider_BuildProductLandsOnKeyPath(t *testing.T) {
	scope := appkit.ApplyScope{Config: &bootstrapv1.BootstrapConfig{
		Instrumentation: &bootstrapv1.Instrumentation{
			Prometheus: &bootstrapv1.Instrumentation_Prometheus{Namespace: "built"},
		},
	}}
	product, cleanup, err := appProvider{}.Build(context.Background(), scope, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup 应为 nil：Prometheus 后端没有可关闭资源")
	}
	m, ok := product.(map[string]any)
	if !ok {
		t.Fatalf("product type = %T, want map[string]any", product)
	}
	p, ok := m["prometheus"].(*prom.Provider)
	if !ok {
		t.Fatalf("product[prometheus] type = %T, want *prometheus.Provider", m["prometheus"])
	}
	// 键名 + 顶层段名拼出的资源路径必须与导出句柄的 Key 同形——不一致的话
	// 消费侧 Prometheus.Resource(app) 永远取不到，而且不报错。
	if got := "instrumentation.prometheus"; got != string(Prometheus.Name()) {
		t.Fatalf("资源路径 %q 与句柄名 %q 不一致", got, Prometheus.Name())
	}
	if p.Registry() == nil {
		t.Fatal("Registry() 为 nil：产物不是可用的埋点实例")
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
