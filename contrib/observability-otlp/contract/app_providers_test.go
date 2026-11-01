package contract

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/appkit"
)

func TestAppProviders_DeclaresTracerAndMetrics(t *testing.T) {
	var d appkit.Decl
	AppProviders("app-test").Apply(&d)
	if len(d.Providers) != 2 {
		t.Fatalf("providers = %d, want 2 (tracer + metrics)", len(d.Providers))
	}
	got := map[string]bool{}
	for _, p := range d.Providers {
		for _, s := range p.Segments() {
			got[s] = true
		}
	}
	if !got["tracer"] || !got["metrics"] {
		t.Fatalf("segments = %v, want tracer+metrics", got)
	}
}

func TestAppProviders_Nodes(t *testing.T) {
	var d appkit.Decl
	AppProviders("app-test").Apply(&d)

	want := map[string]struct {
		priority   int
		effect     string
		teardownTo []appkit.ResourceKey
	}{
		"tracer":  {priority: appkit.PriorityTracer, effect: "appkit:tracer-shutdown", teardownTo: []appkit.ResourceKey{"logger", "metrics"}},
		"metrics": {priority: appkit.PriorityMetrics, effect: "appkit:metrics-server", teardownTo: []appkit.ResourceKey{"logger"}},
	}
	for _, p := range d.Providers {
		nodes, err := p.Plan(context.Background(), appkit.PlanScope{})
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if len(nodes) != 1 {
			t.Fatalf("plan nodes = %d, want 1", len(nodes))
		}
		n := nodes[0]
		w, ok := want[string(n.Key)]
		if !ok {
			t.Fatalf("unexpected node key %q", n.Key)
		}
		if n.Phase != appkit.PhaseBootstrap || n.Priority != w.priority || n.EffectName != w.effect {
			t.Fatalf("node %q = {phase:%s priority:%d effect:%q}, want bootstrap/%d/%q",
				n.Key, n.Phase, n.Priority, n.EffectName, w.priority, w.effect)
		}
		if len(n.TeardownAfter) != len(w.teardownTo) {
			t.Fatalf("node %q teardown = %v, want %v", n.Key, n.TeardownAfter, w.teardownTo)
		}
	}
}

func TestAppProviders_Build(t *testing.T) {
	var d appkit.Decl
	AppProviders("app-test").Apply(&d)
	bySeg := func(seg string) appkit.Provider {
		for _, p := range d.Providers {
			if p.Segments()[0] == seg {
				return p
			}
		}
		t.Fatalf("provider for %q not found", seg)
		return nil
	}

	// 段缺省：不装配。
	if _, _, err := bySeg("tracer").Build(context.Background(),
		appkit.ApplyScope{Config: &bootstrapv1.BootstrapConfig{}, Decl: &appkit.Decl{}}, appkit.Node{}); err != nil {
		t.Fatalf("tracer absent should be no-op: %v", err)
	}

	// 未知 type：fail-fast 并报已注册值。
	_, _, err := bySeg("tracer").Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{Tracer: &bootstrapv1.Tracer{Type: "zipkin"}},
		Decl:   &appkit.Decl{},
	}, appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "zipkin") {
		t.Fatalf("unknown tracer.type should fail with type, got: %v", err)
	}
	_, _, err = bySeg("metrics").Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{Metrics: &bootstrapv1.Metrics{Type: "statsd"}},
		Decl:   &appkit.Decl{},
	}, appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "statsd") {
		t.Fatalf("unknown metrics.type should fail with type, got: %v", err)
	}

	// prometheus：暴露端非 nil，cleanup（flush）可执行。
	product, cleanup, err := bySeg("metrics").Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{Metrics: &bootstrapv1.Metrics{
			Type:       TypePrometheus,
			Prometheus: &bootstrapv1.Metrics_Prometheus{Addr: "127.0.0.1:0"},
		}},
		Decl: &appkit.Decl{},
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("prometheus build: %v", err)
	}
	exp, ok := product.(appkit.MetricsExposure)
	if !ok || exp == nil {
		t.Fatalf("product %T, want non-nil MetricsExposure", product)
	}
	if err := exp.Stop(context.Background()); err != nil {
		t.Fatalf("stop exposure: %v", err)
	}
	if cleanup == nil {
		t.Fatal("metrics cleanup should carry flush")
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
}
