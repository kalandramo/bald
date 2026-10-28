package appkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/registry"
)

// standardProviders 等价于 std.Standard()，但避免包内测试 import std
// （std import appkit，包内测试再 import std 即环）。
func standardProviders() Option {
	return Providers(StandardServerProvider(), StandardAuditProvider(), StandardLoggerProvider())
}

// writePlanConfig 落一份最小契约（app.name 必填；其余段由 bconf 预填）。
func writePlanConfig(t *testing.T, name, extra string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "app:\n  name: \"" + name + "\"\n" + extra
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// planCountingProvider 记录 Build 是否被调用（I1 的副作用探针）。
type planCountingProvider struct {
	segs  []string
	calls *int32
}

func (p planCountingProvider) Segments() []string { return p.segs }
func (p planCountingProvider) Apply(d *Decl)      { d.Providers = append(d.Providers, p) }
func (p planCountingProvider) Plan(context.Context, PlanScope) ([]Node, error) {
	segs := sortedUnique(p.segs)
	top := segmentTop(segs[0])
	return []Node{{
		Key:      nodeKey(segs),
		Phase:    phaseFor(phaseByTop(top)),
		Priority: testSegmentPriority(top),
	}}, nil
}
func (p planCountingProvider) Build(context.Context, ApplyScope, Node) (any, Cleanup, error) {
	atomic.AddInt32(p.calls, 1)
	return nil, nil, nil
}

// planLogger 是全局 logger 的哨兵（指针身份可比较；Compile 改它即被抓）。
type planLogger struct{ id int }

func (*planLogger) Debug(context.Context, string, ...any) {}
func (*planLogger) Info(context.Context, string, ...any)  {}
func (*planLogger) Warn(context.Context, string, ...any)  {}
func (*planLogger) Error(context.Context, string, ...any) {}
func (*planLogger) Enabled(log.Level) bool                { return false }
func (l *planLogger) With(...any) log.Logger              { return l }

// planComponent 是组件节点的最小实现。
type planComponent struct{ name string }

func (c planComponent) Name() string                { return c.name }
func (planComponent) Start(context.Context) error   { return nil }
func (planComponent) Dispose(context.Context) error { return nil }

// testRegistrar 是最小 Registrar（计划只关心「程序化接管」这一事实）。
type testRegistrar struct{}

func (testRegistrar) Register(context.Context, *registry.ServiceInstance) error   { return nil }
func (testRegistrar) Deregister(context.Context, *registry.ServiceInstance) error { return nil }

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name()
	}
	return out
}

// I1：Compile 无副作用——不改全局 logger、不调用 Provider.Build、不跑钩子、
// 不写文件。
func TestPlan_NoSideEffects(t *testing.T) {
	cfgPath := writePlanConfig(t, "plan-canary", "database:\n  sql:\n    driver: \"sqlite\"\n    source: \"file::memory:?cache=shared&_fk=1\"\n")

	oldLogger := log.GetLogger()
	sentinel := &planLogger{id: 1}
	log.SetLogger(sentinel)
	defer log.SetLogger(oldLogger)

	var builds, hooks int32
	p, err := Compile(context.Background(),
		ConfigFile(cfgPath),
		planCountingProvider{segs: []string{"database.sql"}, calls: &builds},
		WithHTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})),
		BeforeStart(func(context.Context) error { atomic.AddInt32(&hooks, 1); return nil }),
		AfterStart(func(context.Context) error { atomic.AddInt32(&hooks, 1); return nil }),
	)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if p == nil {
		t.Fatal("Compile returned nil plan")
	}
	if got := log.GetLogger(); got != sentinel {
		t.Fatalf("Compile changed the global logger: got %T", got)
	}
	if n := atomic.LoadInt32(&builds); n != 0 {
		t.Fatalf("Compile called Provider.Build %d time(s)", n)
	}
	if n := atomic.LoadInt32(&hooks); n != 0 {
		t.Fatalf("Compile ran %d lifecycle hook(s)", n)
	}
	// 配置文件所在目录只应包含配置文件本身（无计划落盘、无临时文件）。
	before := dirEntries(t, filepath.Dir(cfgPath))
	if len(before) != 1 || before[0] != "config.yaml" {
		t.Fatalf("unexpected files in config dir: %v", before)
	}
}

// I2：不改变覆盖关系的 Option 置换 → 同一 Plan.Hash（排序键里没有声明下标）。
func TestPlan_OrderInvariant(t *testing.T) {
	cfgPath := writePlanConfig(t, "plan-order", "database:\n  sql:\n    driver: \"sqlite\"\n    source: \"file::memory:?cache=shared&_fk=1\"\n")

	var builds int32
	db := planCountingProvider{segs: []string{"database.sql"}, calls: &builds}
	base := []Option{
		standardProviders(),
		db,
		ConfigFile(cfgPath),
		WatchConfigFile(true),
		WithUnimplementedSegments("script"),
		Domain{Name: "alpha"},
		Domain{Name: "beta"},
		componentsOpt(planComponent{name: "comp.a"}, planComponent{name: "comp.b"}),
	}

	want, err := Compile(context.Background(), base...)
	if err != nil {
		t.Fatalf("baseline Compile: %v", err)
	}
	wantHash := want.Hash()

	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 100; i++ {
		perm := append([]Option(nil), base...)
		rng.Shuffle(len(perm), func(a, b int) { perm[a], perm[b] = perm[b], perm[a] })
		got, err := Compile(context.Background(), perm...)
		if err != nil {
			t.Fatalf("permutation %d: Compile: %v", i, err)
		}
		if got.Hash() != wantHash {
			t.Fatalf("permutation %d produced a different plan hash:\nwant %s\ngot  %s", i, wantHash, got.Hash())
		}
	}
}

// I3：每个出现的契约段要么有消费者、要么被白名单显式承认；未覆盖段编译失败；
// 节点依赖不得悬空/成环。
func TestPlan_SegmentCoverage(t *testing.T) {
	t.Run("uncovered segment fails", func(t *testing.T) {
		cfgPath := writePlanConfig(t, "plan-uncovered", "script:\n  options:\n    enabled: true\n")
		if _, err := Compile(context.Background(), ConfigFile(cfgPath)); err == nil {
			t.Fatal("Compile succeeded with an uncovered script section; want fail-fast")
		} else if !strings.Contains(err.Error(), "script") {
			t.Fatalf("error does not name the uncovered segment: %v", err)
		}
	})

	t.Run("whitelist is visible", func(t *testing.T) {
		cfgPath := writePlanConfig(t, "plan-whitelist", "script:\n  options:\n    enabled: true\n")
		p, err := Compile(context.Background(), ConfigFile(cfgPath), WithUnimplementedSegments("script"))
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		su := segmentUseByPath(p, "script")
		if su == nil || !su.Whitelisted {
			t.Fatalf("script segment not shown as whitelisted: %+v", su)
		}
		if !hasWarning(p, "whitelisted", "script") {
			t.Fatalf("whitelisted segment missing from Plan.Warnings: %+v", p.Warnings)
		}
	})

	t.Run("complete and dependencies resolve", func(t *testing.T) {
		cfgPath := writePlanConfig(t, "plan-complete", "database:\n  sql:\n    driver: \"sqlite\"\n    source: \"file::memory:?cache=shared&_fk=1\"\n")
		var builds int32
		p, err := Compile(context.Background(),
			standardProviders(),
			planCountingProvider{segs: []string{"database.sql"}, calls: &builds},
			ConfigFile(cfgPath),
			WithHTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})),
			Domain{
				Name:     "greet",
				Routes:   func(http.Handler) error { return nil },
				Services: func(*grpc.Server) {},
			},
		)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		for _, su := range p.Segments {
			if len(su.Consumers) == 0 && !su.Framework && !su.Whitelisted {
				t.Fatalf("segment %q is neither consumed nor whitelisted nor framework-consumed", su.Path)
			}
		}
		for _, path := range []string{"app", "server", "logger", "database"} {
			if segmentUseByPath(p, path) == nil {
				t.Fatalf("present segment %q missing from Plan.Segments", path)
			}
		}
		// 节点依赖必须指向同计划内的节点（拓扑排序已拒绝悬空/成环）。
		byKey := map[ResourceKey]bool{}
		for _, n := range p.Nodes {
			byKey[n.Key] = true
		}
		for _, n := range p.Nodes {
			for _, dep := range n.DependsOn {
				if !byKey[dep] {
					t.Fatalf("node %q depends on missing node %q", n.Key, dep)
				}
			}
		}
		if !hasNode(p, "domain.greet.routes") || !hasNode(p, "domain.greet.services") {
			t.Fatalf("domain routing/service nodes missing: %+v", p.Nodes)
		}
	})

	t.Run("json and text formats", func(t *testing.T) {
		cfgPath := writePlanConfig(t, "plan-formats", "")
		p, err := Compile(context.Background(), ConfigFile(cfgPath))
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		b, err := p.JSON()
		if err != nil {
			t.Fatalf("JSON: %v", err)
		}
		if !json.Valid(b) {
			t.Fatal("Plan.JSON returned invalid JSON")
		}
		var buf bytes.Buffer
		if err := writePlan(&buf, p, "text"); err != nil {
			t.Fatalf("writePlan(text): %v", err)
		}
		if !strings.Contains(buf.String(), "nodes:") || !strings.Contains(buf.String(), p.Hash()) {
			t.Fatalf("text plan missing expected sections:\n%s", buf.String())
		}
		if err := writePlan(&buf, p, "yaml"); err == nil {
			t.Fatal("writePlan accepted an unknown format")
		}
	})
}

func segmentUseByPath(p *Plan, path string) *SegmentUse {
	for i := range p.Segments {
		if p.Segments[i].Path == path {
			return &p.Segments[i]
		}
	}
	return nil
}

func hasWarning(p *Plan, code, subject string) bool {
	for _, w := range p.Warnings {
		if w.Code == code && w.Subject == subject {
			return true
		}
	}
	return false
}

func hasNode(p *Plan, key ResourceKey) bool {
	for _, n := range p.Nodes {
		if n.Key == key {
			return true
		}
	}
	return false
}

// TestParsePlanFlag 钉住 --plan/--plan-format 的 pflag 容错路径（未知 flag
// 不阻断、默认 text）。
func TestParsePlanFlag(t *testing.T) {
	old := os.Args
	defer func() { os.Args = old }()

	os.Args = []string{"app", "--plan"}
	if out := parsePlanFlag(); !out.enabled || out.format != "text" {
		t.Fatalf("--plan parse = %+v, want enabled text", out)
	}
	os.Args = []string{"app", "--plan", "--plan-format=json", "--unknown=1"}
	if out := parsePlanFlag(); !out.enabled || out.format != "json" {
		t.Fatalf("--plan-format parse = %+v, want enabled json", out)
	}
	os.Args = []string{"app"}
	if out := parsePlanFlag(); out.enabled {
		t.Fatalf("--plan parse without flag = %+v, want disabled", out)
	}
}

// TestPlan_RegistrarTakeoverVisible 钉住 I10 的 M0 子集：程序化接管产生合成
// registry 节点（Override + Reason）与警告，regmap 段仍列为被该节点读取。
func TestPlan_RegistrarTakeoverVisible(t *testing.T) {
	cfgPath := writePlanConfig(t, "plan-registrar", "registry:\n  type: \"nacos\"\n  nacos:\n    server_addrs: [\"127.0.0.1:8848\"]\n")
	p, err := Compile(context.Background(), ConfigFile(cfgPath), Registrar(testRegistrar{}))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	var found bool
	for _, n := range p.Nodes {
		if n.Key == "registry" {
			found = true
			if !n.Override || !strings.Contains(n.Reason, "programmatic") {
				t.Fatalf("registry node not marked as takeover: %+v", n)
			}
		}
	}
	if !found {
		t.Fatalf("synthetic registry node missing: %+v", p.Nodes)
	}
	if !hasWarning(p, "programmatic-takeover", "registry") {
		t.Fatalf("takeover warning missing: %+v", p.Warnings)
	}
	if !reflect.DeepEqual(segmentUseByPath(p, "registry"), &SegmentUse{
		Path: "registry", Consumers: []ResourceKey{"registry"},
	}) {
		t.Fatalf("registry segment usage = %+v", segmentUseByPath(p, "registry"))
	}
}

// TestPlan_EscapeHatchesReported 钉住 I10：计划可静态观测的逃逸舱都必须
// 出现在 Plan.Warnings——白名单段、节点 override、程序化 Registrar 接管、
// 环境注入（WithAmbient：build tag / init() 接线）。计划看不到的域仍由
// Opaque 计数与预算纳管（见 TestPlan_OpaqueBudget）。
func TestPlan_EscapeHatchesReported(t *testing.T) {
	t.Run("whitelisted segment", func(t *testing.T) {
		cfgPath := writePlanConfig(t, "plan-escape-whitelist", "script:\n  options:\n    enabled: true\n")
		p, err := Compile(context.Background(), ConfigFile(cfgPath), WithUnimplementedSegments("script"))
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		if !hasWarning(p, "whitelisted", "script") {
			t.Fatalf("whitelist warning missing: %+v", p.Warnings)
		}
	})

	t.Run("node override", func(t *testing.T) {
		cfgPath := writePlanConfig(t, "plan-escape-override",
			"database:\n  sql:\n    driver: \"sqlite\"\n    source: \"file::memory:?cache=shared&_fk=1\"\n"+
				"script:\n  options:\n    enabled: true\n")
		winner := Node{Key: "shared", Kind: "script-impl", Phase: PhaseBootstrap, Priority: PriorityLegacy}
		winner.Override, winner.Reason = true, "test override"
		loser := Node{Key: "shared", Kind: "database-impl", Phase: PhaseBootstrap, Priority: PriorityDatabase}
		p, err := Compile(context.Background(), ConfigFile(cfgPath),
			fixedNodeProvider([]string{"script"}, winner),
			fixedNodeProvider([]string{"database.sql"}, loser),
		)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		if !hasWarning(p, "override", "shared") {
			t.Fatalf("override warning missing: %+v", p.Warnings)
		}
	})

	t.Run("programmatic takeover", func(t *testing.T) {
		cfgPath := writePlanConfig(t, "plan-escape-takeover",
			"registry:\n  type: \"nacos\"\n  nacos:\n    server_addrs: [\"127.0.0.1:8848\"]\n")
		p, err := Compile(context.Background(), ConfigFile(cfgPath), Registrar(testRegistrar{}))
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		if !hasWarning(p, "programmatic-takeover", "registry") {
			t.Fatalf("takeover warning missing: %+v", p.Warnings)
		}
	})

	t.Run("ambient injection", func(t *testing.T) {
		cfgPath := writePlanConfig(t, "plan-escape-ambient", "script:\n  options:\n    enabled: true\n")
		notes := []Ambient{
			{Name: "build-tag:grpcgw", Detail: "grpc-gateway wiring compiled only under -tags grpcgw"},
			{Name: "build-tag:nacos", Detail: "nacos providers compiled only under -tags nacos"},
		}
		compile := func(t *testing.T, notes ...Ambient) *Plan {
			t.Helper()
			p, err := Compile(context.Background(), ConfigFile(cfgPath),
				WithUnimplementedSegments("script"), WithAmbient(notes...))
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			return p
		}

		p := compile(t, notes...)
		for _, n := range notes {
			if !hasWarning(p, "ambient", n.Name) {
				t.Fatalf("ambient warning for %q missing: %+v", n.Name, p.Warnings)
			}
		}
		// 声明序无关（I2）：倒序声明得到同一计划哈希。
		reversed := compile(t, notes[1], notes[0])
		if p.Hash() != reversed.Hash() {
			t.Fatalf("ambient declaration order changed plan hash: %s vs %s", p.Hash(), reversed.Hash())
		}
		// 重复声明去重：同一逃逸舱只产一条诊断。
		dup := compile(t, notes[0], notes[0])
		var ambientCount int
		for _, w := range dup.Warnings {
			if w.Code == "ambient" && w.Subject == notes[0].Name {
				ambientCount++
			}
		}
		if ambientCount != 1 {
			t.Fatalf("duplicate ambient note produced %d warnings, want 1: %+v", ambientCount, dup.Warnings)
		}
	})
}

// TestPlan_AmbientRequiresName 钉住逃逸舱的最小身份约束：无名 Ambient 在
// 计划期报错——诊断无法被引用，等于隐身。
func TestPlan_AmbientRequiresName(t *testing.T) {
	cfgPath := writePlanConfig(t, "plan-ambient-noname", "script:\n  options:\n    enabled: true\n")
	_, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithUnimplementedSegments("script"), WithAmbient(Ambient{Detail: "unlabeled wiring"}))
	if err == nil || !strings.Contains(err.Error(), "ambient declaration requires a Name") {
		t.Fatalf("Compile err = %v, want ambient Name error", err)
	}
}

// TestPlan_OpaqueBudget 钉住逃逸舱的计量出口：OpaqueCount 恒进计划（I10），
// 超预算只产计划警告、不阻断装配；0 = 不设预算，CI 可自行设门。
func TestPlan_OpaqueBudget(t *testing.T) {
	cfgPath := writePlanConfig(t, "plan-opaque",
		"database:\n  sql:\n    driver: \"sqlite\"\n    source: \"file::memory:?cache=shared&_fk=1\"\n"+
			"script:\n  options:\n    enabled: true\n")
	legacy := []Option{
		AdaptLegacy(legacyStub{segs: []string{"database.sql"}}),
		AdaptLegacy(legacyStub{segs: []string{"script"}}),
	}

	compile := func(t *testing.T, opts ...Option) *Plan {
		t.Helper()
		all := append([]Option{ConfigFile(cfgPath)}, opts...)
		p, err := Compile(context.Background(), append(all, legacy...)...)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		return p
	}

	t.Run("count visible without budget", func(t *testing.T) {
		p := compile(t)
		var opaque []ResourceKey
		for _, n := range p.Nodes {
			if n.Opaque {
				opaque = append(opaque, n.Key)
			}
		}
		if p.OpaqueCount != len(opaque) {
			t.Fatalf("OpaqueCount = %d, opaque nodes = %v", p.OpaqueCount, opaque)
		}
		if p.OpaqueCount < 2 || !hasNode(p, "database.sql") || !hasNode(p, "script") {
			t.Fatalf("legacy nodes missing or not counted opaque: count=%d nodes=%+v", p.OpaqueCount, p.Nodes)
		}
		if hasWarning(p, "opaque-budget", "opaque") {
			t.Fatalf("unmanaged plan emitted an opaque-budget warning: %+v", p.Warnings)
		}
	})

	t.Run("over budget warns", func(t *testing.T) {
		p := compile(t, WithOpaqueBudget(1))
		if !hasWarning(p, "opaque-budget", "opaque") {
			t.Fatalf("opaque-budget warning missing: %+v", p.Warnings)
		}
	})

	t.Run("within budget silent", func(t *testing.T) {
		base := compile(t)
		p := compile(t, WithOpaqueBudget(base.OpaqueCount))
		if hasWarning(p, "opaque-budget", "opaque") {
			t.Fatalf("warning for a plan within budget: %+v", p.Warnings)
		}
	})

	t.Run("negative budget rejected", func(t *testing.T) {
		_, err := Compile(context.Background(), ConfigFile(cfgPath), WithOpaqueBudget(-1))
		if err == nil || !strings.Contains(err.Error(), "budget must be >= 0") {
			t.Fatalf("negative budget not rejected: %v", err)
		}
	})

	t.Run("explain prints count", func(t *testing.T) {
		p := compile(t)
		var buf bytes.Buffer
		p.Explain(&buf)
		want := fmt.Sprintf("opaque: %d node(s)", p.OpaqueCount)
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("Explain does not report %q:\n%s", want, buf.String())
		}
	})
}
