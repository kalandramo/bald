package appkit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
)

// planNodeByKey 取计划中的节点（测试断言用）。
func planNodeByKey(p *Plan, key ResourceKey) *Node {
	for i := range p.Nodes {
		if p.Nodes[i].Key == key {
			return &p.Nodes[i]
		}
	}
	return nil
}

// M3：可协调节点（logger/audit/组件）声明 ReloadReconcile 并带期望态摘要；
// 重启/静态节点不产生摘要。
func TestPlan_ReloadPoliciesAndHashes(t *testing.T) {
	cfgPath := writePlanConfig(t, "plan-reload",
		"logger:\n  backends:\n    - type: slog\naudit:\n  backends: [log]\n")
	p, err := Compile(context.Background(),
		ConfigFile(cfgPath),
		standardProviders(),
		componentsOpt(planComponent{name: "probe"}),
	)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	for _, key := range []ResourceKey{"logger", "audit", "component.probe"} {
		n := planNodeByKey(p, key)
		if n == nil {
			t.Fatalf("node %q missing from plan", key)
		}
		if n.Reload != ReloadReconcile {
			t.Errorf("%s reload = %s, want reconcile", key, n.Reload)
		}
		if n.DesiredHash == "" {
			t.Errorf("%s has no DesiredHash", key)
		}
	}
	for _, key := range []ResourceKey{"app", "server"} {
		n := planNodeByKey(p, key)
		if n == nil {
			t.Fatalf("node %q missing from plan", key)
		}
		if n.Reload == ReloadReconcile || n.DesiredHash != "" {
			t.Errorf("%s = reload %s hash %q, want static without hash", key, n.Reload, n.DesiredHash)
		}
	}
}

// 期望态摘要只随节点声明段的内容变化（logger 段改级 → logger 摘要变化，
// audit/组件摘要不动）。
func TestPlan_DesiredHashTracksSegment(t *testing.T) {
	base := "logger:\n  backends:\n    - type: slog\n      slog:\n        level: info\naudit:\n  backends: [log]\n"
	changed := "logger:\n  backends:\n    - type: slog\n      slog:\n        level: debug\naudit:\n  backends: [log]\n"

	compile := func(extra string) *Plan {
		t.Helper()
		p, err := Compile(context.Background(),
			ConfigFile(writePlanConfig(t, "plan-hash", extra)),
			standardProviders(),
			componentsOpt(planComponent{name: "probe"}),
		)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		return p
	}
	a, b := compile(base), compile(changed)
	if planNodeByKey(a, "logger").DesiredHash == planNodeByKey(b, "logger").DesiredHash {
		t.Fatal("logger DesiredHash did not change with logger segment content")
	}
	if planNodeByKey(a, "audit").DesiredHash != planNodeByKey(b, "audit").DesiredHash {
		t.Fatal("audit DesiredHash changed although audit segment is identical")
	}
	if planNodeByKey(a, "component.probe").DesiredHash != planNodeByKey(b, "component.probe").DesiredHash {
		t.Fatal("component DesiredHash changed although component declaration is identical")
	}
}

// diff：新增/移除/摘要变化/依赖变化；相同计划为空。
func TestDiffPlan(t *testing.T) {
	old := &Plan{Nodes: []Node{
		{Key: "app", Kind: "app", Reload: ReloadStatic},
		{Key: "logger", Kind: "logger", Reload: ReloadReconcile, DesiredHash: "h1"},
		{Key: "database.sql", Kind: "database", Reload: ReloadRestart, DesiredHash: "d1"},
	}}
	next := &Plan{Nodes: []Node{
		{Key: "app", Kind: "app", Reload: ReloadStatic, DependsOn: []ResourceKey{"database.sql"}},
		{Key: "logger", Kind: "logger", Reload: ReloadReconcile, DesiredHash: "h2"},
		{Key: "cache.redis", Kind: "cache", Reload: ReloadRestart, DesiredHash: "c1"},
	}}
	c := diffPlan(old, next)
	if len(c.Changes) != 4 {
		t.Fatalf("changes = %+v, want 4 (app deps, logger, db removed, cache added)", c.Changes)
	}
	if !c.Changes[0].DepsChanged || c.Changes[0].Key != "app" {
		t.Errorf("app dependency change missing: %+v", c.Changes[0])
	}
	if diffPlan(next, next).Empty() != true {
		t.Error("identical plans produced a change")
	}
}

// 配置变更应用：协调节点调用 NodeReconciler 并落账摘要；重启节点只进
// Restart 列表；静态节点不动作。
func TestApplyPlanChange_ReconcileAndRestart(t *testing.T) {
	a := NewHarness()
	var calls int32
	a.nodeReconcilers["audit"] = func(context.Context, *Plan) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	old := &Plan{Nodes: []Node{
		{Key: "audit", Kind: "audit", Reload: ReloadReconcile, DesiredHash: "a1"},
		{Key: "database.sql", Kind: "database", Reload: ReloadRestart, DesiredHash: "d1"},
		{Key: "app", Kind: "app", Reload: ReloadStatic},
	}}
	next := &Plan{Nodes: []Node{
		{Key: "audit", Kind: "audit", Reload: ReloadReconcile, DesiredHash: "a2"},
		{Key: "database.sql", Kind: "database", Reload: ReloadRestart, DesiredHash: "d2"},
		{Key: "app", Kind: "app", Reload: ReloadStatic},
	}}
	a.installPlan(old)
	a.setAppliedHash("audit", "a1")

	change := a.applyPlanChange(context.Background(), next, nil)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("reconciler calls = %d, want 1", n)
	}
	if len(change.Restart) != 1 || change.Restart[0] != "database.sql" {
		t.Fatalf("restart list = %v, want [database.sql]", change.Restart)
	}
	a.planMu.RLock()
	got := a.applied["audit"]
	a.planMu.RUnlock()
	if got != "a2" {
		t.Fatalf("applied audit hash = %q, want a2", got)
	}
	if d, err := a.Drift(context.Background()); err != nil || len(d) != 0 {
		t.Fatalf("Drift after apply = %+v, err=%v; want none", d, err)
	}
}

// 节点从新计划消失 = 拓扑变化：协调器没有「拆除节点」的动作，旧资源会存活
// 到进程退出。如实报重启，不静默忘记（Drift 对新计划无从报起）。
func TestApplyPlanChange_RemovedReconcileNodeRequiresRestart(t *testing.T) {
	a := NewHarness()
	old := &Plan{Nodes: []Node{
		{Key: "audit", Kind: "audit", Reload: ReloadReconcile, DesiredHash: "a1"},
	}}
	a.installPlan(old)
	a.setAppliedHash("audit", "a1")

	change := a.applyPlanChange(context.Background(), &Plan{}, nil)
	if len(change.Restart) != 1 || change.Restart[0] != "audit" {
		t.Fatalf("restart list = %v, want [audit]", change.Restart)
	}
	a.planMu.RLock()
	_, stillApplied := a.applied["audit"]
	a.planMu.RUnlock()
	if stillApplied {
		t.Fatal("removed node kept its applied hash")
	}
}

// I7：Drift 必须报 Missing（未落账/组件未启动）、Stale（摘要落后）、
// Unexpected（协调器挂载了计划外组件）。
func TestDrift_DetectsMissing(t *testing.T) {
	a := NewHarness()
	a.installPlan(&Plan{Nodes: []Node{
		{Key: "logger", Kind: "logger", Reload: ReloadReconcile, DesiredHash: "h1"},
		{Key: "component.job", Kind: "component", Reload: ReloadReconcile, DesiredHash: "c1"},
	}})

	drift, err := a.Drift(context.Background())
	if err != nil {
		t.Fatalf("Drift: %v", err)
	}
	if len(drift) != 2 {
		t.Fatalf("drift = %+v, want 2 missing", drift)
	}
	for _, d := range drift {
		if d.Kind != DriftMissing {
			t.Fatalf("drift[%s] kind = %s, want missing", d.Key, d.Kind)
		}
	}

	// 落账 + 组件启动后漂移消失。
	a.setAppliedHash("logger", "h1")
	a.setAppliedHash("component.job", "c1")
	a.startedMu.Lock()
	a.started = append(a.started, planComponent{name: "job"})
	a.startedMu.Unlock()
	if drift, _ := a.Drift(context.Background()); len(drift) != 0 {
		t.Fatalf("drift after apply = %+v, want none", drift)
	}

	// 摘要落后 = Stale。
	a.setAppliedHash("logger", "old")
	drift, _ = a.Drift(context.Background())
	if len(drift) != 1 || drift[0].Kind != DriftStale || drift[0].Key != "logger" {
		t.Fatalf("drift = %+v, want stale logger", drift)
	}

	// 协调器挂载的计划外组件 = Unexpected。
	a.setAppliedHash("logger", "h1")
	a.reconItems["recon"] = append(a.reconItems["recon"], reconItem{name: "ghost"})
	drift, _ = a.Drift(context.Background())
	if len(drift) != 1 || drift[0].Kind != DriftUnexpected || drift[0].Key != "component.ghost" {
		t.Fatalf("drift = %+v, want unexpected component.ghost", drift)
	}
}

func TestDrift_RequiresRunning(t *testing.T) {
	a := NewApp()
	if _, err := a.Drift(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Drift on idle App = %v, want ErrNotRunning", err)
	}
}

// reconcileProbeProvider 是可协调节点探针：Build 记录调用序，不产生资源。
type reconcileProbeProvider struct {
	first  chan struct{}
	builds *int32
}

func (p reconcileProbeProvider) Segments() []string { return []string{"script"} }
func (p reconcileProbeProvider) Apply(d *Decl)      { d.Providers = append(d.Providers, p) }
func (p reconcileProbeProvider) Plan(context.Context, PlanScope) ([]Node, error) {
	return []Node{{
		Key:      "script.probe",
		Kind:     "script",
		Segments: []string{"script"},
		Phase:    PhaseBootstrap,
		Reload:   ReloadReconcile,
	}}, nil
}
func (p reconcileProbeProvider) Build(context.Context, ApplyScope, Node) (any, Cleanup, error) {
	if n := atomic.AddInt32(p.builds, 1); n == 1 && p.first != nil {
		close(p.first)
	}
	return nil, nil, nil
}

// 协调器只在阶段 B 注册：config/server 段的 Provider 节点声明 reconcile
// 将来只会得到一个永远 Missing 的漂移记录——计划期即拒绝。
func TestPlan_RejectsReconcileOnNonBootstrapProvider(t *testing.T) {
	configProvider := segProvider{segs: []string{"config"}, plan: func(context.Context, PlanScope) ([]Node, error) {
		return []Node{{Key: "config.probe", Phase: PhaseConfigure, Reload: ReloadReconcile}}, nil
	}}
	_, err := Compile(context.Background(),
		ConfigFile(writePlanConfig(t, "reconcile-config-phase", "")), configProvider)
	if err == nil || !strings.Contains(err.Error(), "reload=reconcile") {
		t.Fatalf("Compile(config provider reconcile) = %v, want reload=reconcile rejection", err)
	}

	cfg := bconf.NewBootstrap()
	cfg.Server.Asynq = &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"}
	serverProvider := segProvider{segs: []string{"server.asynq"}, plan: func(context.Context, PlanScope) ([]Node, error) {
		return []Node{{Key: "server.asynq", Phase: PhaseServe, Reload: ReloadReconcile}}, nil
	}}
	_, err = compilePlan(context.Background(), newDecl(serverProvider), cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "reload=reconcile") {
		t.Fatalf("Compile(server provider reconcile) = %v, want reload=reconcile rejection", err)
	}
}

// e2e：契约层变更 → 计划重算 → 可协调节点重建 → Drift 收敛为空。
func TestHotReload_ReconcilesPlan(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	path := filepath.Join(t.TempDir(), "reload.yaml")
	writeConfig := func(enabled bool) {
		t.Helper()
		doc := "app:\n  name: \"reload-e2e\"\nscript:\n  options:\n    enabled: " + strconv.FormatBool(enabled) + "\n"
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	writeConfig(true)

	cfg := bconf.NewBootstrap()
	cfg.Script = &bootstrapv1.Script{Options: &bootstrapv1.Script_EngineOptions{Enabled: true}}

	var builds int32
	first := make(chan struct{})
	a := assembleForTest(t, cfg,
		Name("reload-e2e"),
		ConfigFile(path),
		WatchConfigFile(true),
		reconcileProbeProvider{first: first, builds: &builds},
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return")
		}
	})

	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("initial plan did not build the reconcile node")
	}
	if drift, err := a.Drift(context.Background()); err != nil || len(drift) != 0 {
		t.Fatalf("initial drift = %+v, err=%v; want none", drift, err)
	}

	time.Sleep(20 * time.Millisecond)
	writeConfig(false)
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&builds) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("config change did not reconcile the plan node (builds=%d enabled=%v)",
				atomic.LoadInt32(&builds), cfg.GetScript().GetOptions().GetEnabled())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if drift, err := a.Drift(context.Background()); err != nil || len(drift) != 0 {
		t.Fatalf("drift after reload = %+v, err=%v; want none", drift, err)
	}
}
