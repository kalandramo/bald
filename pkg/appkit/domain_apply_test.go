// domain_apply_test.go 钉住 Domain.ApplyNode（设计 §4.6 目标形态的执行点）：
// 声明节点由引擎按计划序逐条执行，域不再产生 Opaque 兼容节点；与兼容槽位
// 混用、声明节点标 Opaque、有 ApplyNode 无 Declare 都在装配/计划期 fail-fast。
package appkit

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	bconf "github.com/kalandramo/bald/bconf"
)

var errApplyNode = errors.New("apply failed")

func TestDomain_ApplyNodeValidation(t *testing.T) {
	declareOne := func(context.Context, PlanScope) ([]Node, error) {
		return []Node{{Key: "domain.greet.biz", Phase: PhaseWire}}, nil
	}
	cases := []struct {
		name string
		dom  Domain
		want string
	}{
		{
			name: "no declare",
			dom:  Domain{Name: "greet", ApplyNode: func(context.Context, *App, Node) error { return nil }},
			want: "declares ApplyNode but no Declare",
		},
		{
			name: "mixed with wire",
			dom: Domain{
				Name:      "greet",
				Declare:   declareOne,
				ApplyNode: func(context.Context, *App, Node) error { return nil },
				Wire:      func(context.Context, *App) error { return nil },
			},
			want: "mixes ApplyNode with compat hooks",
		},
		{
			name: "mixed with routes",
			dom: Domain{
				Name:      "greet",
				Declare:   declareOne,
				ApplyNode: func(context.Context, *App, Node) error { return nil },
				Routes:    func(http.Handler) error { return nil },
			},
			want: "mixes ApplyNode with compat hooks",
		},
		{
			name: "declares server.grpc node",
			dom: Domain{
				Name: "greet",
				Declare: func(context.Context, PlanScope) ([]Node, error) {
					return []Node{{Key: "domain.greet.services", Segments: []string{"server.grpc"}, Phase: PhaseServe}}, nil
				},
				ApplyNode: func(context.Context, *App, Node) error { return nil },
				Services:  func(*grpc.Server) {},
			},
			want: "server.grpc cannot be applied in the domain window",
		},
		{
			name: "opaque declared node",
			dom: Domain{
				Name: "greet",
				Declare: func(context.Context, PlanScope) ([]Node, error) {
					return []Node{{Key: "domain.greet.biz", Phase: PhaseWire, Opaque: true}}, nil
				},
				ApplyNode: func(context.Context, *App, Node) error { return nil },
			},
			want: "must declare non-opaque nodes",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Compile(context.Background(), ConfigFile(writePlanConfig(t, "apply-node", "")), c.dom)
			if err == nil {
				t.Fatalf("Compile accepted invalid ApplyNode domain")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want substring %q", err, c.want)
			}
		})
	}
}

// ApplyNode 域的声明节点进入计划且非 Opaque；不再合成兼容接线节点。
func TestDomain_ApplyNodeProjectsNoCompatNode(t *testing.T) {
	cfgPath := writePlanConfig(t, "apply-projection", "")
	dom := Domain{
		Name: "greet",
		Declare: func(context.Context, PlanScope) ([]Node, error) {
			return []Node{{
				Key:      "domain.greet.biz",
				Segments: []string{"logger"},
				Phase:    PhaseWire,
			}}, nil
		},
		ApplyNode: func(context.Context, *App, Node) error { return nil },
	}
	p, err := Compile(context.Background(), ConfigFile(cfgPath), dom)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	n := nodeByKey(p, "domain.greet.biz")
	if n == nil {
		t.Fatalf("declared node missing: %+v", p.Nodes)
	}
	if n.Opaque {
		t.Fatal("ApplyNode node must not be Opaque")
	}
	if nodeByKey(p, "domain.greet.wire") != nil {
		t.Fatal("ApplyNode domain must not synthesize a compat node")
	}
	// 迁移完成的域不贡献逃逸舱：唯一剩下的 Opaque 是框架自带的 logger 节点。
	for _, n := range p.Nodes {
		if n.Opaque && n.Origin == OriginDomain {
			t.Fatalf("domain node %q still Opaque after ApplyNode migration", n.Key)
		}
	}
	if p.OpaqueCount != 1 {
		t.Fatalf("opaque count = %d, want 1 (framework logger only)", p.OpaqueCount)
	}
}

// Services 是延迟注册回调（server 构建时执行），与 ApplyNode 共存合法：
// 不产生兼容节点，也不要求域声明 server.grpc 节点。
func TestDomain_ApplyNodeAllowsDeferredServices(t *testing.T) {
	cfgPath := writePlanConfig(t, "apply-services", "server:\n  grpc:\n    addr: \"127.0.0.1:0\"\n")
	dom := Domain{
		Name: "greet",
		Declare: func(context.Context, PlanScope) ([]Node, error) {
			return []Node{{Key: "domain.greet.biz", Phase: PhaseWire}}, nil
		},
		ApplyNode: func(context.Context, *App, Node) error { return nil },
		Services:  func(*grpc.Server) {},
	}
	p, err := Compile(context.Background(), ConfigFile(cfgPath), dom)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if nodeByKey(p, "domain.greet.biz") == nil {
		t.Fatalf("declared node missing: %+v", p.Nodes)
	}
	if nodeByKey(p, "domain.greet.wire") != nil {
		t.Fatal("ApplyNode domain must not synthesize a compat node")
	}
	if nodeByKey(p, "domain.greet.services") != nil {
		t.Fatal("services are registered by Domain.Services; the plan must not project a node for them")
	}
}

// ApplyNode 在装配路径上能拿到 WithHTTP 声明的 handler（路由节点的挂载入口）。
// kitOpts 不携带业务能力声明，App 的大门必须从 spec 补上——否则路由节点执行时
// 拿到 nil，域要么静默少挂路由、要么报错退出。
func TestDomain_ApplyNodeSeesHTTPHandler(t *testing.T) {
	h := new(http.ServeMux)
	var got atomic.Value
	dom := Domain{
		Name: "greet",
		Declare: func(context.Context, PlanScope) ([]Node, error) {
			return []Node{{Key: "domain.greet.biz", Phase: PhaseWire}}, nil
		},
		ApplyNode: func(_ context.Context, app *App, _ Node) error {
			got.Store(app.HTTPHandler())
			return nil
		},
	}
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	a := assembleForTest(t, cfg, WithHTTP(h), dom)
	runBriefly(t, a, 50*time.Millisecond)

	if got.Load() != http.Handler(h) {
		t.Fatalf("ApplyNode saw HTTP handler %v, want the declared one", got.Load())
	}
}

// 执行序 = 计划序（Phase → Priority → Key），不是声明序。
func TestDomain_ApplyNodeExecutesInPlanOrder(t *testing.T) {
	var mu struct {
		keys []string
	}
	dom := Domain{
		Name: "greet",
		Declare: func(context.Context, PlanScope) ([]Node, error) {
			return []Node{
				{Key: "domain.greet.routes", Phase: PhaseServe, Priority: 10},
				{Key: "domain.greet.biz", Phase: PhaseWire, Priority: 10},
			}, nil
		},
		ApplyNode: func(_ context.Context, _ *App, node Node) error {
			mu.keys = append(mu.keys, string(node.Key))
			return nil
		},
	}
	a := assembleForTest(t, bconf.NewBootstrap(), dom)
	runBriefly(t, a, 50*time.Millisecond)

	want := []string{"domain.greet.biz", "domain.greet.routes"}
	if strings.Join(mu.keys, ",") != strings.Join(want, ",") {
		t.Fatalf("apply order = %v, want %v", mu.keys, want)
	}
}

// ApplyNode 是 reconcile 节点的构造点：执行成功后节点记为已应用（Drift 不误报）。
func TestDomain_ApplyNodeMarksReconcileApplied(t *testing.T) {
	var applied int32
	dom := Domain{
		Name: "greet",
		Declare: func(context.Context, PlanScope) ([]Node, error) {
			return []Node{{
				Key:      "domain.greet.limiter",
				Segments: []string{"logger"},
				Phase:    PhaseWire,
				Reload:   ReloadReconcile,
			}}, nil
		},
		ApplyNode: func(context.Context, *App, Node) error {
			atomic.AddInt32(&applied, 1)
			return nil
		},
		Reconcile: func(context.Context, *App, Node) error { return nil },
	}
	a := assembleForTest(t, bconf.NewBootstrap(), dom)
	if a.nodeReconcilers["domain.greet.limiter"] == nil {
		t.Fatal("declared reconcile node has no registered reconciler")
	}
	runBriefly(t, a, 50*time.Millisecond)

	if n := atomic.LoadInt32(&applied); n != 1 {
		t.Fatalf("apply calls = %d, want 1", n)
	}
	a.planMu.RLock()
	appliedHash := a.applied["domain.greet.limiter"]
	a.planMu.RUnlock()
	if appliedHash == "" {
		t.Fatal("declared reconcile node not marked applied after ApplyNode")
	}
}

// 节点执行失败 = 启动失败（错误带域名与节点 Key），不静默跳过。
func TestDomain_ApplyNodeFailureIsFailFast(t *testing.T) {
	dom := Domain{
		Name: "greet",
		Declare: func(context.Context, PlanScope) ([]Node, error) {
			return []Node{{Key: "domain.greet.biz", Phase: PhaseWire}}, nil
		},
		ApplyNode: func(_ context.Context, _ *App, node Node) error {
			return errApplyNode
		},
	}
	a := assembleForTest(t, bconf.NewBootstrap(), dom)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run succeeded despite ApplyNode failure")
		}
		for _, want := range []string{`domain "greet"`, `apply "domain.greet.biz"`, errApplyNode.Error()} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want substring %q", err, want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ApplyNode failure")
	}
}
