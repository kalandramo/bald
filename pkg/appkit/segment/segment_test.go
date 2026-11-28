package segment

import (
	"context"
	"reflect"
	"testing"

	"github.com/kalandramo/bald/pkg/appkit"
)

// TestFamilyDefaultsMatchPilots 钉住段族表与 contract 包现行手写 Plan 字面量
// 逐字段一致（A4 判据的单元形态：试点迁移后既有集成测试不改一字仍绿）。
func TestFamilyDefaultsMatchPilots(t *testing.T) {
	cases := []struct {
		path string
		want appkit.Node
	}{
		{"server.cron", appkit.Node{Key: "server.cron", Segments: []string{"server.cron"}, Phase: appkit.PhaseServe}},
		{"server.mcp", appkit.Node{Key: "server.mcp", Segments: []string{"server.mcp"}, Phase: appkit.PhaseServe}},
		{"cache.redis", appkit.Node{Key: "cache.redis", Segments: []string{"cache.redis"}, Phase: appkit.PhaseBootstrap, Priority: appkit.PriorityCache, Reload: appkit.ReloadRestart, EffectName: "appkit:cache-clients"}},
		{"registry.etcd", appkit.Node{Key: "registry.etcd", Segments: []string{"registry.etcd"}, Phase: appkit.PhaseBootstrap, Priority: appkit.PriorityRegistry, Reload: appkit.ReloadRestart, EffectName: "appkit:registrar-client"}},
		{"broker.kafka", appkit.Node{Key: "broker.kafka", Segments: []string{"broker.kafka"}, Phase: appkit.PhaseBootstrap, Priority: appkit.PriorityBroker, Reload: appkit.ReloadRestart, EffectName: "appkit:broker-clients"}},
		{"instrumentation.otlp", appkit.Node{Key: "instrumentation.otlp", Segments: []string{"instrumentation.otlp"}, Phase: appkit.PhaseBootstrap, Priority: appkit.PriorityMetrics, Reload: appkit.ReloadRestart, EffectName: "appkit:instrumentation-otlp"}},
		{"instrumentation.prometheus", appkit.Node{Key: "instrumentation.prometheus", Segments: []string{"instrumentation.prometheus"}, Phase: appkit.PhaseBootstrap, Priority: appkit.PriorityMetrics, Reload: appkit.ReloadRestart, EffectName: "appkit:instrumentation-prometheus"}},
		{"storage.s3", appkit.Node{Key: "storage.s3", Segments: []string{"storage.s3"}, Phase: appkit.PhaseBootstrap, Priority: appkit.PriorityStorage, Reload: appkit.ReloadRestart, EffectName: "appkit:storage-clients"}},
		{"tracer", appkit.Node{Key: "tracer", Segments: []string{"tracer"}, Phase: appkit.PhaseBootstrap, Priority: appkit.PriorityTracer, Reload: appkit.ReloadRestart, EffectName: "appkit:tracer-shutdown"}},
		{"client", appkit.Node{Key: "client", Segments: []string{"client"}, Phase: appkit.PhaseBootstrap, Priority: appkit.PriorityClient, Reload: appkit.ReloadStatic}},
		// S4 补族：值 = stdAuditProvider / admin appBindingProvider 手写字面量
		// （不含 Kind/Origin 身份位——由声明者经 Opt 显式给，见迁移点）。
		{"audit", appkit.Node{Key: "audit", Segments: []string{"audit"}, Phase: appkit.PhaseBootstrap, Priority: appkit.PriorityAudit, Reload: appkit.ReloadReconcile, TeardownAfter: []appkit.ResourceKey{"*"}, EffectName: "appkit:audit-restore"}},
		{"app", appkit.Node{Key: "app", Segments: []string{"app"}, Phase: appkit.PhaseBootstrap}},
	}
	nop := func(context.Context, appkit.ApplyScope, appkit.Node) (any, appkit.Cleanup, error) {
		return nil, nil, nil
	}
	for _, c := range cases {
		got, err := New(c.path, nop).Plan(context.Background(), appkit.PlanScope{})
		if err != nil {
			t.Fatalf("%s: Plan: %v", c.path, err)
		}
		if len(got) != 1 {
			t.Fatalf("%s: Plan 节点数 = %d, want 1", c.path, len(got))
		}
		if !reflect.DeepEqual(got[0], c.want) {
			t.Errorf("%s: 派生节点与手写字面量不一致\n got = %+v\nwant = %+v", c.path, got[0], c.want)
		}
		if !reflect.DeepEqual(New(c.path, nop).Segments(), []string{c.path}) {
			t.Errorf("%s: Segments() 不匹配", c.path)
		}
	}
}

// TestOptsOverride 例外字段逐项覆盖生效。
func TestOptsOverride(t *testing.T) {
	nop := func(context.Context, appkit.ApplyScope, appkit.Node) (any, appkit.Cleanup, error) {
		return nil, nil, nil
	}
	// TeardownAfter 双向：族缺省（audit ["*"]）可被覆盖清空或替换。
	auditNode, err := New("audit", nop).Plan(context.Background(), appkit.PlanScope{})
	if err != nil {
		t.Fatalf("audit Plan: %v", err)
	}
	if !reflect.DeepEqual(auditNode[0].TeardownAfter, []appkit.ResourceKey{"*"}) {
		t.Errorf("audit 族缺省 TeardownAfter = %v, want [*]", auditNode[0].TeardownAfter)
	}
	over, err := New("audit", nop, TeardownAfter("database.sql")).Plan(context.Background(), appkit.PlanScope{})
	if err != nil {
		t.Fatalf("audit override Plan: %v", err)
	}
	if !reflect.DeepEqual(over[0].TeardownAfter, []appkit.ResourceKey{"database.sql"}) {
		t.Errorf("TeardownAfter 覆盖 = %v, want [database.sql]", over[0].TeardownAfter)
	}
	p := New("server.gateway", nop,
		Key("gw"), Kind("server"), Segments("server.gateway", "client"),
		At(appkit.PhaseWire), Priority(7), Reload(appkit.ReloadReconcile),
		Effect("custom-eff"), Opaque(), DependsOn("cache.redis"),
	)
	d := &appkit.Decl{}
	p.Apply(d)
	if len(d.Providers) != 1 {
		t.Fatalf("Apply 后 Providers 数 = %d, want 1", len(d.Providers))
	}
	if !reflect.DeepEqual(p.Segments(), []string{"server.gateway", "client"}) {
		t.Errorf("Segments 覆盖失败: %v", p.Segments())
	}
	nodes, err := d.Providers[0].Plan(context.Background(), appkit.PlanScope{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	n := nodes[0]
	want := appkit.Node{
		Key: "gw", Kind: "server", Segments: []string{"server.gateway", "client"},
		DependsOn: []appkit.ResourceKey{"cache.redis"},
		Phase:     appkit.PhaseWire, Priority: 7, Reload: appkit.ReloadReconcile,
		Opaque: true, EffectName: "custom-eff",
	}
	if !reflect.DeepEqual(n, want) {
		t.Errorf("覆盖后节点 = %+v, want %+v", n, want)
	}
}

// TestBuildDelegates Build 原样委托主体（含 cleanup 透传）。
func TestBuildDelegates(t *testing.T) {
	called := false
	p := New("cache.local", func(context.Context, appkit.ApplyScope, appkit.Node) (any, appkit.Cleanup, error) {
		called = true
		return "prod", func(context.Context) error { return nil }, nil
	})
	out, cleanup, err := p.Build(context.Background(), appkit.ApplyScope{}, appkit.Node{})
	if !called || err != nil || out != "prod" || cleanup == nil {
		t.Fatalf("Build 委托失真: called=%v out=%v cleanup=%v err=%v", called, out, cleanup, err)
	}
}

// TestNewPanicsOnBadWiring 接线期错误当场炸（未知族 / nil build / 空路径）。
func TestNewPanicsOnBadWiring(t *testing.T) {
	nop := func(context.Context, appkit.ApplyScope, appkit.Node) (any, appkit.Cleanup, error) {
		return nil, nil, nil
	}
	for _, c := range []struct {
		name string
		run  func()
	}{
		{"未知段族", func() { New("widget.foo", nop) }},
		{"nil build", func() { New("server.x", nil) }},
		{"空路径", func() { New("", nop) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: 应 panic 未 panic", c.name)
				}
			}()
			c.run()
		})
	}
}
