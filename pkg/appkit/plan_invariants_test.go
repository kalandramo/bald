// plan_invariants_test.go —— I4（冲突显式）与 I5（停机序）的回归锁。
//
// I4：同 Key 节点必须靠唯一 Override + Reason 消歧，计划里留下 override
// 诊断；无 Override 或双 Override 都是编译失败。
// I5：停机序 = Apply 逆序受 TeardownAfter 修正；用随机 DAG 验证「所有约束
// 都被满足且每个节点恰好释放一次」，并用 std 声明钉住历史回放序。
package appkit

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// fixedNodeProvider 返回声明固定节点的测试 Provider（Build 不产生资源）。
func fixedNodeProvider(segs []string, n Node) segProvider {
	return segProvider{
		segs:  segs,
		plan:  func(context.Context, PlanScope) ([]Node, error) { return []Node{n}, nil },
		build: func(context.Context, BuildScope) (any, Cleanup, error) { return nil, nil, nil },
	}
}

// I4：同 Key 三方会审——无 Override 报错、唯一 Override + Reason 胜出并留
// 诊断、Override 缺 Reason 报错、双 Override 报错。
func TestPlan_Conflict(t *testing.T) {
	cfgPath := writePlanConfig(t, "plan-conflict",
		"database:\n  sql:\n    driver: \"sqlite\"\n    source: \"file::memory:?cache=shared&_fk=1\"\n"+
			"script:\n  options:\n    enabled: true\n")

	scriptNode := Node{Key: "shared", Kind: "script-impl", Phase: PhaseBootstrap, Priority: PriorityLegacy}
	dbNode := Node{Key: "shared", Kind: "database-impl", Phase: PhaseBootstrap, Priority: PriorityDatabase}
	scriptProv := fixedNodeProvider([]string{"script"}, scriptNode)
	dbProv := fixedNodeProvider([]string{"database.sql"}, dbNode)

	t.Run("no override fails", func(t *testing.T) {
		_, err := Compile(context.Background(), ConfigFile(cfgPath), scriptProv, dbProv)
		if err == nil {
			t.Fatal("Compile succeeded with a duplicate node key and no Override; want fail-fast")
		}
		if !strings.Contains(err.Error(), "duplicate node key") || !strings.Contains(err.Error(), "shared") {
			t.Fatalf("error does not name the conflicting key: %v", err)
		}
	})

	t.Run("single override wins with warning", func(t *testing.T) {
		winner := scriptNode
		winner.Override = true
		winner.Reason = "test override"
		p, err := Compile(context.Background(),
			ConfigFile(cfgPath),
			fixedNodeProvider([]string{"script"}, winner),
			dbProv,
		)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		var got []Node
		for _, n := range p.Nodes {
			if n.Key == "shared" {
				got = append(got, n)
			}
		}
		if len(got) != 1 || got[0].Kind != "script-impl" || !got[0].Override {
			t.Fatalf("override survivor = %+v, want exactly the script node", got)
		}
		if !hasWarning(p, "override", "shared") {
			t.Fatalf("override diagnostic missing from Plan.Warnings: %+v", p.Warnings)
		}
	})

	t.Run("override without reason fails", func(t *testing.T) {
		winner := scriptNode
		winner.Override = true
		_, err := Compile(context.Background(),
			ConfigFile(cfgPath),
			fixedNodeProvider([]string{"script"}, winner),
			dbProv,
		)
		if err == nil || !strings.Contains(err.Error(), "no Reason") {
			t.Fatalf("Compile = %v, want missing-Reason failure", err)
		}
	})

	t.Run("two overrides fail", func(t *testing.T) {
		a, b := scriptNode, dbNode
		a.Override, a.Reason = true, "a"
		b.Override, b.Reason = true, "b"
		_, err := Compile(context.Background(),
			ConfigFile(cfgPath),
			fixedNodeProvider([]string{"script"}, a),
			fixedNodeProvider([]string{"database.sql"}, b),
		)
		if err == nil || !strings.Contains(err.Error(), "at most one override") {
			t.Fatalf("Compile = %v, want multiple-override failure", err)
		}
	})
}

// I5 基础语义：无 TeardownAfter 时停机序 = Apply 逆序。
func TestTeardownOrder_ApplyReverse(t *testing.T) {
	nodes := []Node{{Key: "a"}, {Key: "b"}, {Key: "c"}}
	got, err := teardownOrder(nodes)
	if err != nil {
		t.Fatalf("teardownOrder: %v", err)
	}
	want := []ResourceKey{"c", "b", "a"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("teardown order = %v, want %v", got, want)
	}
}

// I5 约束语义：TearDownAfter 把节点推迟到被依赖者之后；未知键视为已满足。
func TestTeardownOrder_Constraints(t *testing.T) {
	nodes := []Node{
		{Key: "logger"},
		{Key: "metrics", TeardownAfter: []ResourceKey{"logger"}},
		{Key: "tracer", TeardownAfter: []ResourceKey{"logger", "metrics"}},
		{Key: "database", TeardownAfter: []ResourceKey{"does-not-exist"}},
		{Key: "audit", TeardownAfter: []ResourceKey{"*"}},
	}
	got, err := teardownOrder(nodes)
	if err != nil {
		t.Fatalf("teardownOrder: %v", err)
	}
	want := []ResourceKey{"database", "logger", "metrics", "tracer", "audit"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("teardown order = %v, want %v", got, want)
	}
}

// I5 拒绝成环与重复键。
func TestTeardownOrder_RejectsCyclesAndDuplicates(t *testing.T) {
	if _, err := teardownOrder([]Node{
		{Key: "a", TeardownAfter: []ResourceKey{"b"}},
		{Key: "b", TeardownAfter: []ResourceKey{"a"}},
	}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("teardownOrder cycle = %v, want cycle error", err)
	}
	if _, err := teardownOrder([]Node{{Key: "a"}, {Key: "a"}}); err == nil {
		t.Fatal("teardownOrder accepted duplicate keys")
	}
}

// I5 随机 DAG：约束图由随机拓扑序生成（必然无环），断言输出是排列且每条
// TeardownAfter 都满足。
func TestTeardownOrder_RandomDAG(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for iter := 0; iter < 200; iter++ {
		n := 1 + rng.Intn(8)
		nodes := make([]Node, n)
		for i := range nodes {
			nodes[i] = Node{Key: ResourceKey(fmt.Sprintf("n%d", i))}
		}
		// perm 定义 DAG 的合法序：边只从靠前者指向靠后者。
		perm := rng.Perm(n)
		pos := make(map[int]int, n)
		for i, idx := range perm {
			pos[idx] = i
		}
		for k := 0; k < n; k++ {
			if rng.Intn(2) == 0 {
				continue
			}
			u, v := rng.Intn(n), rng.Intn(n)
			if u == v {
				continue
			}
			if pos[u] > pos[v] {
				u, v = v, u
			}
			// v 在 u 之后释放。
			nodes[v].TeardownAfter = append(nodes[v].TeardownAfter, nodes[u].Key)
		}

		got, err := teardownOrder(nodes)
		if err != nil {
			t.Fatalf("iteration %d: teardownOrder: %v", iter, err)
		}
		seen := make(map[ResourceKey]bool, len(got))
		at := make(map[ResourceKey]int, len(got))
		for i, k := range got {
			if seen[k] {
				t.Fatalf("iteration %d: key %q released twice", iter, k)
			}
			seen[k] = true
			at[k] = i
		}
		if len(seen) != n {
			t.Fatalf("iteration %d: %d keys released, want %d", iter, len(seen), n)
		}
		for _, node := range nodes {
			for _, dep := range node.TeardownAfter {
				if at[dep] >= at[node.Key] {
					t.Fatalf("iteration %d: %q released before its prerequisite %q", iter, node.Key, dep)
				}
			}
		}
	}
}

// legacyStub 是 v1 Provider 的最小声明的形态（只有 Segments + Build）。
type legacyStub struct {
	segs  []string
	built *bool
}

func (l legacyStub) Segments() []string { return l.segs }
func (legacyStub) Apply(*Decl)          {}
func (l legacyStub) Build(context.Context, BuildScope) (any, Cleanup, error) {
	if l.built != nil {
		*l.built = true
	}
	return nil, nil, nil
}

// AdaptLegacy：v1 Provider 以 Opaque legacy 节点进入计划（Key 由段路径合成、
// 优先级落在 PriorityLegacy），Compile 不触发 Build；Build 委托原实现。
func TestAdaptLegacy_PlansOpaqueNode(t *testing.T) {
	cfgPath := writePlanConfig(t, "plan-legacy",
		"database:\n  sql:\n    driver: \"sqlite\"\n    source: \"file::memory:?cache=shared&_fk=1\"\n")
	var built bool
	prov := AdaptLegacy(legacyStub{segs: []string{"database.sql"}, built: &built})
	p, err := Compile(context.Background(), ConfigFile(cfgPath), prov)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if built {
		t.Fatal("Compile invoked a legacy Provider.Build (I1)")
	}
	var node *Node
	for i := range p.Nodes {
		if p.Nodes[i].Key == "database.sql" {
			node = &p.Nodes[i]
		}
	}
	if node == nil {
		t.Fatalf("legacy node missing from plan: %+v", p.Nodes)
	}
	if !node.Opaque || node.Origin != OriginLegacy || node.Priority != PriorityLegacy || node.Phase != PhaseBootstrap {
		t.Fatalf("legacy node = %+v, want opaque/legacy/bootstrap@%d", *node, PriorityLegacy)
	}
	if _, _, err := prov.Build(context.Background(), ApplyScope{}, *node); err != nil || !built {
		t.Fatalf("AdaptLegacy.Build did not delegate: err=%v built=%v", err, built)
	}
}

// I5/std 声明：审计 ["*"] + 指标 ["logger"] + 追踪 ["logger","metrics"]
// 推出的停机序必须与旧手工表（cleanupOrder）的历史回放序一致——
// database → logger → metrics → tracer → audit。
func TestProviderPlan_StdTeardownOrder(t *testing.T) {
	cfg := &bootstrapv1.BootstrapConfig{
		Audit:    &bootstrapv1.Audit{Backends: []string{AuditTypeLog}},
		Metrics:  &bootstrapv1.Metrics{},
		Tracer:   &bootstrapv1.Tracer{},
		Database: &bootstrapv1.Database{},
	}
	d := &Decl{Providers: []Provider{
		StandardLoggerProvider(),
		StandardAuditProvider(),
		segProvider{segs: []string{"metrics"}, build: func(context.Context, BuildScope) (any, Cleanup, error) { return nil, nil, nil }},
		segProvider{segs: []string{"tracer"}, build: func(context.Context, BuildScope) (any, Cleanup, error) { return nil, nil, nil }},
		segProvider{segs: []string{"database"}, build: func(context.Context, BuildScope) (any, Cleanup, error) { return nil, nil, nil }},
	}}
	plan, err := newProviderPlan(context.Background(), d, cfg)
	if err != nil {
		t.Fatalf("newProviderPlan: %v", err)
	}
	want := []ResourceKey{"database", "logger", "metrics", "tracer", "audit"}
	if fmt.Sprint(plan.teardown) != fmt.Sprint(want) {
		t.Fatalf("std teardown order = %v, want legacy replay order %v", plan.teardown, want)
	}
}
