package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确（instrumentation.otlp，与契约字段名同形）、Option 嵌入面
//      可用、Plan 只有一个与段路径同名的节点；
//   2. 段在 → Build 产物落 map[string]any{"otlp": *}，键名 + 顶层段名拼出的
//      资源路径与导出句柄 Otlp.Name() 同形，cleanup 非 nil；
//   3. 子段缺失 → 复用 Provider 的 fail-fast（不产半成品实例）。
//
// 构造只建 exporter（不建连），所以本文件不需要 collector；上报级判据在
// mapping_offline_test.go。

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	otelmetrics "github.com/kalandramo/bald/metrics/otel"
	"github.com/kalandramo/bald/pkg/appkit"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "instrumentation.otlp" {
		t.Fatalf("Segments() = %v, want [instrumentation.otlp]", segs)
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
	if n.Key != appkit.ResourceKey("instrumentation.otlp") || len(n.Segments) != 1 || n.Segments[0] != string(n.Key) {
		t.Fatalf("Plan 节点 = %+v, want Key==Segments[0]==instrumentation.otlp", n)
	}
	if n.Priority != appkit.PriorityMetrics {
		t.Fatalf("Priority = %d, want PriorityMetrics(%d)", n.Priority, appkit.PriorityMetrics)
	}
	if n.EffectName != "appkit:instrumentation-otlp" {
		t.Fatalf("EffectName = %q, want appkit:instrumentation-otlp", n.EffectName)
	}
}

// 产物键 + 顶层段名拼出的资源路径必须与导出句柄的 Key 同形；app 身份
// 交给 Provider 的回落效果在 mapping_offline_test.go 的上报体里判。
// endpoint 用会应答的假收集器：Build 只建 exporter，但 cleanup 会做末次上报，
// 打到一个不回话的落点上会按 Close 的 10s 上限全额等待（实测见
// mapping_offline_test.go 文件头），本行不需要那份代价。
func TestAppProvider_BuildProductLandsOnKeyPath(t *testing.T) {
	addr, _ := newGRPCCollector(t)
	scope := appkit.ApplyScope{Config: &bootstrapv1.BootstrapConfig{
		App: &bootstrapv1.App{Name: "built-app", Version: "v7.7.7"},
		Instrumentation: &bootstrapv1.Instrumentation{
			Otlp: &bootstrapv1.Instrumentation_Otlp{Endpoint: addr, Insecure: true},
		},
	}}
	product, cleanup, err := appProvider{}.Build(context.Background(), scope, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup = nil, want non-nil（exporter/MeterProvider 要关）")
	}
	m, ok := product.(map[string]any)
	if !ok {
		t.Fatalf("product type = %T, want map[string]any", product)
	}
	if _, ok := m["otlp"].(*otelmetrics.Provider); !ok {
		t.Fatalf("product[otlp] type = %T, want *otel.Provider", m["otlp"])
	}
	if got := "instrumentation.otlp"; got != string(Otlp.Name()) {
		t.Fatalf("资源路径 %q 与句柄名 %q 不一致", got, Otlp.Name())
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
