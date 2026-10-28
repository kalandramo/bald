package appkit

import (
	"bytes"
	"context"
	"encoding/json"
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
