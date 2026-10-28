// domain_reconcile_test.go 钉住 Domain.Reconcile：声明 ReloadReconcile 节点的
// 域必须有协调钩子（否则计划期 fail-fast）；装配期按节点 Key 注册适配器；
// Wire 成功后节点记为已应用（Drift 不误报 Missing）；配置变更时钩子被调用
// 且新摘要落账。
package appkit

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
)

func reconcileDomain(calls *int32) Domain {
	return Domain{
		Name: "greet",
		Declare: func(context.Context, PlanScope) ([]Node, error) {
			return []Node{{
				Key:      "domain.greet.limiter",
				Segments: []string{"logger"},
				Phase:    PhaseWire,
				Reload:   ReloadReconcile,
			}}, nil
		},
		Wire: func(context.Context, *App) error { return nil },
		Reconcile: func(_ context.Context, _ *App, node Node) error {
			if node.Key != "domain.greet.limiter" {
				return nil
			}
			atomic.AddInt32(calls, 1)
			return nil
		},
	}
}

func TestDomain_DeclareReconcileValidation(t *testing.T) {
	base := func() Domain {
		return Domain{
			Name: "greet",
			Declare: func(context.Context, PlanScope) ([]Node, error) {
				return []Node{{
					Key:      "domain.greet.limiter",
					Segments: []string{"logger"},
					Phase:    PhaseWire,
					Reload:   ReloadReconcile,
				}}, nil
			},
		}
	}
	cases := []struct {
		name string
		mut  func(*Domain)
		want string
	}{
		{"no reconcile hook", func(d *Domain) { d.Wire = func(context.Context, *App) error { return nil } }, "requires Domain.Reconcile"},
		{"no wire", func(d *Domain) {
			d.Reconcile = func(context.Context, *App, Node) error { return nil }
		}, "requires Domain.Wire"},
		{"no segments", func(d *Domain) {
			d.Wire = func(context.Context, *App) error { return nil }
			d.Reconcile = func(context.Context, *App, Node) error { return nil }
			dom := d.Declare
			d.Declare = func(ctx context.Context, s PlanScope) ([]Node, error) {
				nodes, err := dom(ctx, s)
				if err == nil {
					nodes[0].Segments = nil
				}
				return nodes, err
			}
		}, "requires at least one segment"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dom := base()
			c.mut(&dom)
			_, err := Compile(context.Background(), ConfigFile(writePlanConfig(t, "declare-reconcile", "")), dom)
			if err == nil {
				t.Fatalf("Compile accepted invalid declarative reconcile node")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want substring %q", err, c.want)
			}
		})
	}
}

// 声明节点 → 所属域的归属在计划期记录（装配期据此注册协调器）。
func TestDomain_DeclareReconcileRecordsOwnership(t *testing.T) {
	cfg := bconf.NewBootstrap()
	var calls int32
	dom := reconcileDomain(&calls)
	d := newDecl(dom)
	pp, err := newProviderPlan(context.Background(), d, cfg)
	if err != nil {
		t.Fatalf("newProviderPlan: %v", err)
	}
	if _, err := planFrom(context.Background(), d, cfg, pp); err != nil {
		t.Fatalf("planFrom: %v", err)
	}
	if got := pp.domainNodes["domain.greet.limiter"].Name; got != "greet" {
		t.Fatalf("declared node owner = %q, want greet", got)
	}
}

// 装配期：协调器已注册；Run 后节点记为已应用；配置变更触发域钩子并落新摘要。
func TestDomain_ReconcileRegisteredAndApplied(t *testing.T) {
	var calls int32
	cfg := bconf.NewBootstrap()
	a := assembleForTest(t, cfg, reconcileDomain(&calls))
	if a.nodeReconcilers["domain.greet.limiter"] == nil {
		t.Fatal("declared reconcile node has no registered reconciler")
	}

	runBriefly(t, a, 100*time.Millisecond)

	a.planMu.RLock()
	applied := a.applied["domain.greet.limiter"]
	a.planMu.RUnlock()
	if applied == "" {
		t.Fatal("declared reconcile node not marked applied after Wire")
	}

	a.planMu.RLock()
	cur := a.plan
	a.planMu.RUnlock()
	next := &Plan{App: cur.App, Role: cur.Role, InputHash: cur.InputHash}
	next.Nodes = append([]Node(nil), cur.Nodes...)
	for i := range next.Nodes {
		if next.Nodes[i].Key == "domain.greet.limiter" {
			next.Nodes[i].DesiredHash = "changed"
		}
	}
	a.applyPlanChange(context.Background(), next, nil)

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("domain reconcile calls = %d, want 1", n)
	}
	a.planMu.RLock()
	applied = a.applied["domain.greet.limiter"]
	a.planMu.RUnlock()
	if applied != "changed" {
		t.Fatalf("applied hash = %q, want changed", applied)
	}
}
