// plan_cache_test.go 钉住 §4.9 计划缓存的四条边界：
//  1. 写入基线后同计划对比为空、报告 "plan unchanged"；
//  2. 节点变化与契约（InputHash）变化分别可见（+ 新增 / input changed）；
//  3. 缓存缺失只提示 "no baseline"，损坏只提示 "unavailable"——都不报错；
//  4. 条目按 (Role, InputHash) 去重并有上限，文件不会无界增长。
package appkit

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlanCache_RoundTripAndDiff(t *testing.T) {
	cfgPath := writePlanConfig(t, "cache-app", "script:\n  options:\n    enabled: true\n")
	alpha := Node{Key: "alpha", Kind: "script-impl", Phase: PhaseBootstrap, Priority: PriorityLegacy}
	p, err := Compile(context.Background(), ConfigFile(cfgPath), fixedNodeProvider([]string{"script"}, alpha))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	path := filepath.Join(t.TempDir(), ".bald", "plan-cache.json")
	now := time.Date(2026, 10, 29, 3, 4, 5, 0, time.UTC)
	if err := writePlanCacheEntry(path, p, now); err != nil {
		t.Fatalf("writePlanCacheEntry: %v", err)
	}

	f, err := readPlanCacheFile(path)
	if err != nil {
		t.Fatalf("readPlanCacheFile: %v", err)
	}
	if f.Version != planCacheVersion || len(f.Entries) != 1 {
		t.Fatalf("cache file = %+v, want version %d with 1 entry", f, planCacheVersion)
	}
	if got := f.Entries[0].PlanHash; got != p.Hash() {
		t.Fatalf("recorded plan hash = %s, want %s", got, p.Hash())
	}
	if got := f.Entries[0].RecordedAt; got != "2026-10-29T03:04:05Z" {
		t.Fatalf("recordedAt = %q, want UTC RFC3339", got)
	}

	// 同一计划：无差异，报告 "plan unchanged"。
	same := comparePlanCache(path, p)
	if same == nil || same.Err != nil || same.Entry == nil {
		t.Fatalf("comparePlanCache = %+v, want baseline entry", same)
	}
	if same.InputChanged || !same.Change.Empty() {
		t.Fatalf("compare with identical plan = %+v, want no change", same)
	}
	if out := renderDoctor(p, same); !strings.Contains(out, "plan unchanged") {
		t.Fatalf("doctor output missing unchanged marker:\n%s", out)
	}
}

// 命令式槽位变化：节点对比为空，但整份计划哈希变了——doctor 不得谎报
// "plan unchanged"（effect/keyWatch/reconcile 同样进 Plan.Hash）。
func TestPlanCache_NonNodeChangeVisible(t *testing.T) {
	cfgPath := writePlanConfig(t, "cache-imperative", "script:\n  options:\n    enabled: true\n")
	alpha := Node{Key: "alpha", Kind: "script-impl", Phase: PhaseBootstrap, Priority: PriorityLegacy}
	compile := func(name string) *Plan {
		t.Helper()
		p, err := Compile(context.Background(), ConfigFile(cfgPath),
			fixedNodeProvider([]string{"script"}, alpha),
			effectOpt(name, func(context.Context) error { return nil }))
		if err != nil {
			t.Fatalf("Compile(%s): %v", name, err)
		}
		return p
	}
	base, next := compile("cleanup-a"), compile("cleanup-b")
	if base.Hash() == next.Hash() {
		t.Fatal("effect rename kept the same plan hash")
	}

	path := filepath.Join(t.TempDir(), "plan-cache.json")
	if err := writePlanCacheEntry(path, base, time.Now()); err != nil {
		t.Fatalf("writePlanCacheEntry: %v", err)
	}
	cmp := comparePlanCache(path, next)
	if cmp.Entry == nil || !cmp.PlanChanged || len(cmp.Change.Changes) != 0 {
		t.Fatalf("compare = %+v, want plan changed with empty node diff", cmp)
	}
	if len(cmp.Change.Imperative) != 2 {
		t.Fatalf("imperative diff = %+v, want one removal + one addition", cmp.Change.Imperative)
	}
	out := renderDoctor(next, cmp)
	if strings.Contains(out, "plan unchanged") || !strings.Contains(out, "imperative slots changed") {
		t.Fatalf("doctor output must report non-node plan change:\n%s", out)
	}
	for _, want := range []string{"- effect: cleanup-a", "+ effect: cleanup-b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
}

// 路由清单变化（Q5：Domain.RouteTable 是声明面的一部分）：节点/槽位对比为
// 空，但 doctor 必须单列 routes changed 并逐条给出增删——换一条 REST 路径
// 同样改变运行期行为，不能在基线对比里隐身。
func TestPlanCache_RouteChangeVisible(t *testing.T) {
	cfgPath := writePlanConfig(t, "cache-routes", "")
	compile := func(routePath string) *Plan {
		t.Helper()
		dom := Domain{Name: "greet", RouteTable: []Route{
			{Method: "POST", Path: routePath, RPC: "Greet"},
		}}
		p, err := Compile(context.Background(), ConfigFile(cfgPath), WithHTTP(http.NewServeMux()), dom)
		if err != nil {
			t.Fatalf("Compile(%s): %v", routePath, err)
		}
		return p
	}
	base, next := compile("/v1/greet"), compile("/v1/greets")
	if base.Hash() == next.Hash() {
		t.Fatal("route path change kept the same plan hash")
	}

	cachePath := filepath.Join(t.TempDir(), "plan-cache.json")
	if err := writePlanCacheEntry(cachePath, base, time.Now()); err != nil {
		t.Fatalf("writePlanCacheEntry: %v", err)
	}
	cmp := comparePlanCache(cachePath, next)
	if !cmp.PlanChanged || !cmp.Change.Empty() {
		t.Fatalf("compare = %+v, want route-only plan change", cmp)
	}
	if len(cmp.Routes) != 2 || !cmp.Routes[0].Removed || !cmp.Routes[1].Added {
		t.Fatalf("route diff = %+v, want one removal + one addition", cmp.Routes)
	}
	out := renderDoctor(next, cmp)
	for _, want := range []string{
		"routes changed: 2",
		"- greet POST /v1/greet -> Greet",
		"+ greet POST /v1/greets -> Greet",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "node attributes") {
		t.Fatalf("route-only change must not be reported as node attributes:\n%s", out)
	}
}

// 变体声明（Q7：AppSpec.variants → Plan.Variants）是声明面的一部分：声明
// 增删与命中态（Active，二进制实际 -tags 决定）变化都改变装配口径，doctor
// 必须单列 variants changed——未命中的声明不产诊断，不能在对比里隐身。
func TestPlanCache_VariantChangeVisible(t *testing.T) {
	cfgPath := writePlanConfig(t, "cache-variants", "script:\n  options:\n    enabled: true\n")
	alpha := Node{Key: "alpha", Kind: "script-impl", Phase: PhaseBootstrap, Priority: PriorityLegacy}
	compile := func(vs ...VariantDecl) *Plan {
		t.Helper()
		opts := []Option{ConfigFile(cfgPath), fixedNodeProvider([]string{"script"}, alpha)}
		if len(vs) > 0 {
			opts = append(opts, WithVariantDecls(vs...))
		}
		p, err := Compile(context.Background(), opts...)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		return p
	}
	base, declared := compile(), compile(VariantDecl{Name: "grpcgw", BuildTags: []string{"grpcgw"}})
	if base.Hash() == declared.Hash() {
		t.Fatal("variant declaration kept the same plan hash")
	}

	path := filepath.Join(t.TempDir(), "plan-cache.json")
	if err := writePlanCacheEntry(path, base, time.Now()); err != nil {
		t.Fatalf("writePlanCacheEntry: %v", err)
	}
	cmp := comparePlanCache(path, declared)
	if !cmp.PlanChanged || !cmp.Change.Empty() {
		t.Fatalf("compare = %+v, want variant-only plan change", cmp)
	}
	if len(cmp.Variants) != 1 || !cmp.Variants[0].Added || cmp.Variants[0].Name != "grpcgw" {
		t.Fatalf("variant diff = %+v, want added grpcgw", cmp.Variants)
	}
	out := renderDoctor(declared, cmp)
	for _, want := range []string{"variants changed: 1", "+ grpcgw", "declared", "tags=grpcgw"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "plan unchanged") || strings.Contains(out, "node attributes") {
		t.Fatalf("variant-only change must not be reported as unchanged/node attributes:\n%s", out)
	}

	// 同一份声明，换成带 tag 的构建：命中态翻转（Active false → true）同样
	// 是装配口径变化；命中的变体另产 ambient 诊断。
	active := compile(VariantDecl{Name: "grpcgw", BuildTags: []string{"grpcgw"}, Active: true})
	if err := writePlanCacheEntry(path, declared, time.Now()); err != nil {
		t.Fatalf("writePlanCacheEntry(declared): %v", err)
	}
	cmp = comparePlanCache(path, active)
	if !cmp.PlanChanged || len(cmp.Variants) != 1 || !cmp.Variants[0].ActiveChanged || !cmp.Variants[0].Active {
		t.Fatalf("compare = %+v, want active-state change on grpcgw", cmp)
	}
	out = renderDoctor(active, cmp)
	for _, want := range []string{"variants changed: 1", "~ grpcgw", "tags=grpcgw: active"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
}

// 节点属性（priority 等）变化会移动装配哈希，但不在节点身份/期望态差异里：
// doctor 不得谎报 "plan unchanged"，也不假装能逐项列出。
func TestPlanCache_NodeAttributeChangeVisible(t *testing.T) {
	cfgPath := writePlanConfig(t, "cache-attribute", "script:\n  options:\n    enabled: true\n")
	compile := func(priority int) *Plan {
		t.Helper()
		p, err := Compile(context.Background(), ConfigFile(cfgPath),
			fixedNodeProvider([]string{"script"},
				Node{Key: "alpha", Kind: "script-impl", Phase: PhaseBootstrap, Priority: priority}))
		if err != nil {
			t.Fatalf("Compile(priority=%d): %v", priority, err)
		}
		return p
	}
	base, next := compile(PriorityLegacy), compile(PriorityLegacy+1)
	path := filepath.Join(t.TempDir(), "plan-cache.json")
	if err := writePlanCacheEntry(path, base, time.Now()); err != nil {
		t.Fatalf("writePlanCacheEntry: %v", err)
	}
	cmp := comparePlanCache(path, next)
	if !cmp.PlanChanged || !cmp.Change.Empty() {
		t.Fatalf("compare = %+v, want attribute-only change", cmp)
	}
	out := renderDoctor(next, cmp)
	if strings.Contains(out, "plan unchanged") || !strings.Contains(out, "node attributes") {
		t.Fatalf("doctor output must report attribute-only change:\n%s", out)
	}
}

// 诊断（ambient 接线）变化不移动节点/槽位，但基线对比必须单列 diagnostics
// ——否则 build tag 分叉在计划缓存里彻底隐身。
func TestPlanCache_DiagnosticsChangeVisible(t *testing.T) {
	cfgPath := writePlanConfig(t, "cache-diagnostics", "script:\n  options:\n    enabled: true\n")
	alpha := Node{Key: "alpha", Kind: "script-impl", Phase: PhaseBootstrap, Priority: PriorityLegacy}
	compile := func(notes ...Ambient) *Plan {
		t.Helper()
		opts := []Option{ConfigFile(cfgPath), fixedNodeProvider([]string{"script"}, alpha)}
		if len(notes) > 0 {
			opts = append(opts, WithAmbient(notes...))
		}
		p, err := Compile(context.Background(), opts...)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		return p
	}
	base, next := compile(), compile(Ambient{
		Name:   "build-tag:grpcgw",
		Detail: "gateway registration injected by -tags grpcgw",
	})
	path := filepath.Join(t.TempDir(), "plan-cache.json")
	if err := writePlanCacheEntry(path, base, time.Now()); err != nil {
		t.Fatalf("writePlanCacheEntry: %v", err)
	}
	cmp := comparePlanCache(path, next)
	if cmp.PlanChanged || !cmp.Change.Empty() {
		t.Fatalf("compare = %+v, want assembly unchanged", cmp)
	}
	if len(cmp.Diagnostics) != 1 || !cmp.Diagnostics[0].Added || cmp.Diagnostics[0].Code != "ambient" {
		t.Fatalf("diagnostic diff = %+v, want one added ambient", cmp.Diagnostics)
	}
	out := renderDoctor(next, cmp)
	for _, want := range []string{
		"plan unchanged: nodes + imperative slots + routes + variants",
		"diagnostics changed: 1",
		"+ warn ambient (build-tag:grpcgw)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestPlanCache_DiffOnAddedNode(t *testing.T) {
	cfgPath := writePlanConfig(t, "cache-diff",
		"script:\n  options:\n    enabled: true\n"+
			"broker:\n  type: \"kafka\"\n")
	alpha := Node{Key: "alpha", Kind: "script-impl", Phase: PhaseBootstrap, Priority: PriorityLegacy}
	base, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithUnimplementedSegments("broker"), fixedNodeProvider([]string{"script"}, alpha))
	if err != nil {
		t.Fatalf("Compile base: %v", err)
	}
	path := filepath.Join(t.TempDir(), "plan-cache.json")
	if err := writePlanCacheEntry(path, base, time.Now()); err != nil {
		t.Fatalf("writePlanCacheEntry: %v", err)
	}

	beta := Node{Key: "beta", Kind: "broker-impl", Phase: PhaseBootstrap, Priority: PriorityLegacy}
	next, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithUnimplementedSegments("broker"),
		fixedNodeProvider([]string{"script"}, alpha),
		fixedNodeProvider([]string{"broker"}, beta))
	if err != nil {
		t.Fatalf("Compile next: %v", err)
	}

	cmp := comparePlanCache(path, next)
	if cmp == nil || cmp.Err != nil || cmp.InputChanged {
		t.Fatalf("comparePlanCache = %+v, want same-input baseline", cmp)
	}
	if cmp.Change.Empty() || len(cmp.Change.Changes) != 1 {
		t.Fatalf("changes = %+v, want exactly the added node", cmp.Change)
	}
	if ch := cmp.Change.Changes[0]; !ch.Added || ch.Key != "beta" {
		t.Fatalf("change = %+v, want added beta", ch)
	}
	out := renderDoctor(next, cmp)
	for _, want := range []string{"plan changed: 1 node(s)", "+ beta (broker-impl, static)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestPlanCache_ContractChangeStillDiffs(t *testing.T) {
	alpha := Node{Key: "alpha", Kind: "script-impl", Phase: PhaseBootstrap, Priority: PriorityLegacy}
	baseCfg := writePlanConfig(t, "cache-contract", "script:\n  options:\n    enabled: true\n")
	base, err := Compile(context.Background(), ConfigFile(baseCfg), fixedNodeProvider([]string{"script"}, alpha))
	if err != nil {
		t.Fatalf("Compile base: %v", err)
	}
	path := filepath.Join(t.TempDir(), "plan-cache.json")
	if err := writePlanCacheEntry(path, base, time.Now()); err != nil {
		t.Fatalf("writePlanCacheEntry: %v", err)
	}

	nextCfg := writePlanConfig(t, "cache-contract",
		"script:\n  options:\n    enabled: true\n"+
			"broker:\n  type: \"kafka\"\n")
	next, err := Compile(context.Background(), ConfigFile(nextCfg),
		WithUnimplementedSegments("broker"), fixedNodeProvider([]string{"script"}, alpha))
	if err != nil {
		t.Fatalf("Compile next: %v", err)
	}
	if next.InputHash == base.InputHash {
		t.Fatalf("test setup: contract change must move InputHash")
	}

	cmp := comparePlanCache(path, next)
	if cmp == nil || cmp.Err != nil || cmp.Entry == nil {
		t.Fatalf("comparePlanCache = %+v, want fallback baseline", cmp)
	}
	if !cmp.InputChanged {
		t.Fatalf("InputChanged = false, want true after contract change")
	}
	if !cmp.Change.Empty() {
		t.Fatalf("node changes = %+v, want none (only warnings differ)", cmp.Change.Changes)
	}
	out := renderDoctor(next, cmp)
	for _, want := range []string{
		"input changed since baseline:",
		// 判据限定在装配声明：告警/段视图变化不冒充「整份计划未变」。
		"plan unchanged: nodes + imperative slots + routes + variants",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestPlanCache_MissingAndCorruptAreDiagnostics(t *testing.T) {
	p := &Plan{App: AppMeta{Name: "cache-empty"}}
	path := filepath.Join(t.TempDir(), "missing.json")

	missing := comparePlanCache(path, p)
	if missing == nil || missing.Entry != nil || missing.Err != nil {
		t.Fatalf("comparePlanCache(missing) = %+v, want empty baseline without error", missing)
	}
	if out := renderDoctor(p, missing); !strings.Contains(out, "no baseline yet") {
		t.Fatalf("doctor output missing no-baseline marker:\n%s", out)
	}

	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write corrupt cache: %v", err)
	}
	corrupt := comparePlanCache(path, p)
	if corrupt == nil || corrupt.Err == nil {
		t.Fatalf("comparePlanCache(corrupt) = %+v, want read error", corrupt)
	}
	if out := renderDoctor(p, corrupt); !strings.Contains(out, "unavailable:") {
		t.Fatalf("doctor output missing unavailable marker:\n%s", out)
	}

	// 写入路径在缓存损坏时重建（损坏不是错误源）。
	if err := writePlanCacheEntry(path, p, time.Now()); err != nil {
		t.Fatalf("writePlanCacheEntry over corrupt cache: %v", err)
	}
	f, err := readPlanCacheFile(path)
	if err != nil || len(f.Entries) != 1 {
		t.Fatalf("rebuilt cache = %+v, err %v; want one entry", f, err)
	}
}

func TestPlanCache_EntriesCappedAndKeyed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan-cache.json")
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	for i := 0; i < planCacheMaxEntries+4; i++ {
		p := &Plan{InputHash: fmt.Sprintf("input-%02d", i)}
		if err := writePlanCacheEntry(path, p, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("writePlanCacheEntry(%d): %v", i, err)
		}
	}
	f, err := readPlanCacheFile(path)
	if err != nil {
		t.Fatalf("readPlanCacheFile: %v", err)
	}
	if len(f.Entries) != planCacheMaxEntries {
		t.Fatalf("entries = %d, want cap %d", len(f.Entries), planCacheMaxEntries)
	}
	if f.Entries[0].InputHash != "input-04" {
		t.Fatalf("oldest kept = %q, want input-04 (four oldest dropped)", f.Entries[0].InputHash)
	}

	// 同 (Role, InputHash) 覆盖而非追加。
	again := &Plan{InputHash: "input-19"}
	if err := writePlanCacheEntry(path, again, now.Add(time.Hour)); err != nil {
		t.Fatalf("writePlanCacheEntry(overwrite): %v", err)
	}
	f, err = readPlanCacheFile(path)
	if err != nil {
		t.Fatalf("readPlanCacheFile: %v", err)
	}
	if len(f.Entries) != planCacheMaxEntries {
		t.Fatalf("entries after overwrite = %d, want %d", len(f.Entries), planCacheMaxEntries)
	}
	if last := f.Entries[len(f.Entries)-1]; last.InputHash != "input-19" || last.RecordedAt != now.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("overwritten entry = %+v, want refreshed record", last)
	}
}

func TestPlanRole_FromWithRole(t *testing.T) {
	cfgPath := writePlanConfig(t, "role-app", "script:\n  options:\n    enabled: true\n")
	var seen []string
	prov := segProvider{
		segs: []string{"script"},
		plan: func(_ context.Context, s PlanScope) ([]Node, error) {
			seen = append(seen, s.Role)
			return []Node{{Key: "alpha", Kind: "script-impl", Phase: PhaseBootstrap, Priority: PriorityLegacy}}, nil
		},
		build: func(context.Context, BuildScope) (any, Cleanup, error) { return nil, nil, nil },
	}
	p, err := Compile(context.Background(), ConfigFile(cfgPath), WithRole("api"), prov)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if p.Role != "api" {
		t.Fatalf("Plan.Role = %q, want api", p.Role)
	}
	if len(seen) != 1 || seen[0] != "api" {
		t.Fatalf("PlanScope.Role seen = %v, want [api]", seen)
	}
}

// renderDoctor 渲染 doctor 报告为字符串（测试共用）。
func renderDoctor(p *Plan, cache *planCacheBaseline) string {
	var buf bytes.Buffer
	writeDoctor(&buf, p, nil, cache, domainReport{})
	return buf.String()
}
