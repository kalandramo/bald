package codegen

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// guardFixture 是字段消费机器守卫与三道门测试的参考夹具：字段取值与
// fieldConsumers 的标记一一同构（改这里必须同步改标记，反之亦然——这正是
// 机器守卫要钉住的对账关系）。
func guardFixture() *appspecv1.AppSpec {
	return &appspecv1.AppSpec{
		Meta: &appspecv1.AppMeta{Name: "demoapp", Module: "demo.example/app", Desc: "demo desc"},
		Bootstrap: &bootstrapv1.BootstrapConfig{
			App:    &bootstrapv1.App{Name: "demoapp"},
			Server: &bootstrapv1.Server{Http: &bootstrapv1.Server_Http{Addr: ":18080"}},
			Audit:  &bootstrapv1.Audit{Backends: []string{"log"}},
		},
		Server:        &appspecv1.ServerSpec{Http: true, HttpAddr: ":18080"},
		AuditBackends: []string{"log"},
		Components: []*appspecv1.ComponentSpec{{
			Kind: "heartbeat", Name: "demo.hb", ConfigPrefix: "demo.hb",
			Params: map[string]string{"interval": "30s"},
		}},
		Capability: &appspecv1.CapabilitySpec{
			Provides: []string{"api", "db"},
			Requires: []*appspecv1.Requirement{{Component: "audit.store", Caps: []string{"db"}}},
		},
		Bundle:           &appspecv1.BundleSpec{Normalized: true},
		BundleNormalized: true,
		Domains: []*appspecv1.DomainSpec{{
			Name: "greet", ProtoPackage: "greet.v1", Service: "greet.v1.GreetService",
			Model: "internal/apiserver/model/greet.go", GoPackage: "demo.example/app/internal/apiserver/greetv1",
		}},
	}
}

// ---------------------------------------------------------------------------
// Gate1 · 生成期 schema 门
// ---------------------------------------------------------------------------

// TestGate1_ValidFixture 全绿基线：参考夹具通过 Gate1。
func TestGate1_ValidFixture(t *testing.T) {
	if errs := validateAppSpec(guardFixture()); len(errs) != 0 {
		t.Fatalf("fixture must pass Gate1, got: %v", errs)
	}
}

// TestGate1_UnknownComponentKind kind 白名单：未实现工厂的 kind 生成期即报
// （含字段名 + 非法值 + 白名单），不再等到运行期 unknown component kind。
func TestGate1_UnknownComponentKind(t *testing.T) {
	as := guardFixture()
	as.Components[0].Kind = "db"
	errs := validateAppSpec(as)
	if len(errs) == 0 {
		t.Fatal("unknown kind must be rejected at generation time")
	}
	msg := joinErrs(errs)
	for _, want := range []string{`components[0].kind "db"`, "heartbeat"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

// TestGate1_DuplicateComponentName 组件名唯一。
func TestGate1_DuplicateComponentName(t *testing.T) {
	as := guardFixture()
	as.Components = append(as.Components, &appspecv1.ComponentSpec{Kind: "heartbeat", Name: "demo.hb"})
	msg := joinErrs(validateAppSpec(as))
	if !strings.Contains(msg, `components[1].name "demo.hb" duplicates components[0].name`) {
		t.Errorf("duplicate component name not reported: %s", msg)
	}
}

// TestGate1_RequiresSubsetOfProvides requires[].caps ⊆ provides（S1 的生成期镜像）。
func TestGate1_RequiresSubsetOfProvides(t *testing.T) {
	as := guardFixture()
	as.Capability.Provides = nil
	msg := joinErrs(validateAppSpec(as))
	for _, want := range []string{"audit.store", `"db"`, "capability.provides"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

// TestGate1_ProjectionConsistency 快捷方式 ⇄ bootstrap 投影（消灭双真相源）：
// 每条违例的报错都带两侧字段路径与取值。
func TestGate1_ProjectionConsistency(t *testing.T) {
	cases := map[string]func(*appspecv1.AppSpec){
		"server.http shortcut without bootstrap projection": func(as *appspecv1.AppSpec) {
			as.Bootstrap.Server = &bootstrapv1.Server{} // http 段缺席
		},
		"server.http_addr mismatch": func(as *appspecv1.AppSpec) {
			as.Bootstrap.Server.Http.Addr = ":19999"
		},
		"server.grpc_addr mismatch": func(as *appspecv1.AppSpec) {
			as.Server.Grpc = true
			as.Server.GrpcAddr = ":19090"
		},
		"audit_backends mismatch": func(as *appspecv1.AppSpec) {
			as.AuditBackends = []string{"log", "store"}
		},
		"audit_backends order matters": func(as *appspecv1.AppSpec) {
			as.Bootstrap.Audit.Backends = []string{"store", "log"}
		},
		"bundle_normalized without bundle.normalized": func(as *appspecv1.AppSpec) {
			as.Bundle = nil
		},
		"gateway without driver projection": func(as *appspecv1.AppSpec) {
			as.Server.Gateway = true
		},
		"meta.name vs bootstrap.app.name": func(as *appspecv1.AppSpec) {
			as.Bootstrap.App.Name = "other"
		},
	}
	for name, mutate := range cases {
		as := guardFixture()
		mutate(as)
		errs := validateAppSpec(as)
		if len(errs) == 0 {
			t.Errorf("%s: projection violation must be rejected", name)
			continue
		}
		msg := joinErrs(errs)
		if !strings.Contains(msg, "must agree") && !strings.Contains(msg, "absent") && !strings.Contains(msg, "at most one place") {
			t.Errorf("%s: error must name the projection conflict, got: %s", name, msg)
		}
	}
}

// TestGate1_GatewayProjectionAccepted gateway 快捷方式的两个合法投影：
// 同端口（driver=grpc-gateway）与独立端口段（bootstrap.server.gateway）。
func TestGate1_GatewayProjectionAccepted(t *testing.T) {
	samePort := guardFixture()
	samePort.Server.Gateway = true
	samePort.Bootstrap.Server.Http.Driver = "grpc-gateway"
	if errs := validateAppSpec(samePort); len(errs) != 0 {
		t.Errorf("driver projection must be accepted: %v", errs)
	}
	independent := guardFixture()
	independent.Server.Gateway = true
	independent.Bootstrap.Server.Gateway = &bootstrapv1.Server_Gateway{Addr: ":18081"}
	if errs := validateAppSpec(independent); len(errs) != 0 {
		t.Errorf("independent gateway segment must be accepted: %v", errs)
	}
}

// TestGate1_Domains 域清单：name 唯一、go_package 必填、import 别名不冲突。
func TestGate1_Domains(t *testing.T) {
	as := guardFixture()
	as.Domains = append(as.Domains, &appspecv1.DomainSpec{Name: "greet"})
	msg := joinErrs(validateAppSpec(as))
	if !strings.Contains(msg, `domains[1].name "greet" duplicates domains[0].name`) {
		t.Errorf("duplicate domain name not reported: %s", msg)
	}

	as2 := guardFixture()
	as2.Domains[0].GoPackage = ""
	msg = joinErrs(validateAppSpec(as2))
	if !strings.Contains(msg, "go_package is required") {
		t.Errorf("missing go_package not reported: %s", msg)
	}

	as3 := guardFixture()
	as3.Domains = append(as3.Domains, &appspecv1.DomainSpec{
		Name: "greet2", GoPackage: "demo.example/app/internal/apiserver/greetv2",
	})
	// greetv2 基名不同 → 别名不冲突，应通过。
	if errs := validateAppSpec(as3); len(errs) != 0 {
		t.Errorf("distinct base names must pass: %v", errs)
	}
	as3.Domains[1].GoPackage = "demo.example/app/other/greetv1" // 基名同为 greetv1
	msg = joinErrs(validateAppSpec(as3))
	if !strings.Contains(msg, "import alias") {
		t.Errorf("alias collision not reported: %s", msg)
	}
}

// TestGate1_MetaRequired meta.name/meta.module 必填（目录树与 import 前缀的根）。
func TestGate1_MetaRequired(t *testing.T) {
	as := guardFixture()
	as.Meta.Name = ""
	as.Meta.Module = ""
	msg := joinErrs(validateAppSpec(as))
	if !strings.Contains(msg, "meta.name is required") || !strings.Contains(msg, "meta.module is required") {
		t.Errorf("required meta fields not reported: %s", msg)
	}
}

// ---------------------------------------------------------------------------
// 快捷方式归一化（bootstrap 为空时的兼容通道）
// ---------------------------------------------------------------------------

// TestNormalizeBootstrap bootstrap 为空时，旧 6 字段快捷方式归一化为契约投影；
// bootstrap 非空时原样透传（一致性已由 Gate1 把关）。
func TestNormalizeBootstrap(t *testing.T) {
	as := &appspecv1.AppSpec{
		Meta:   &appspecv1.AppMeta{Name: "legacy"},
		Server: &appspecv1.ServerSpec{Http: true, HttpAddr: ":18080", Grpc: true, Gateway: true},
		AuditBackends: []string{"log"},
	}
	bs := normalizeBootstrap(as)
	if bs.GetServer().GetHttp() == nil || bs.GetServer().GetHttp().GetAddr() != ":18080" {
		t.Errorf("http projection missing: %v", bs.GetServer())
	}
	if bs.GetServer().GetHttp().GetDriver() != gatewayDriver {
		t.Errorf("gateway shortcut must project to driver=%q, got %q", gatewayDriver, bs.GetServer().GetHttp().GetDriver())
	}
	if bs.GetServer().GetGrpc() == nil {
		t.Error("grpc projection missing")
	}
	if strings.Join(bs.GetAudit().GetBackends(), ",") != "log" {
		t.Errorf("audit projection missing: %v", bs.GetAudit().GetBackends())
	}
	// bootstrap 非空：原样返回（不复制、不改写）。
	orig := guardFixture().Bootstrap
	if got := normalizeBootstrap(guardFixture()); !proto.Equal(got, orig) {
		t.Error("non-empty bootstrap must pass through unchanged")
	}
}

// ---------------------------------------------------------------------------
// Gate2 · 生成期契约门
// ---------------------------------------------------------------------------

// TestGate2_RoundTripGreen 参考夹具：bootstrap → 配置树 → YAML → 回读 →
// 未知键校验全绿；配置含 app.name（缺省取 meta.name 的投影生效）。
func TestGate2_RoundTripGreen(t *testing.T) {
	as := guardFixture()
	cfg, err := contractGate(as, normalizeBootstrap(as))
	if err != nil {
		t.Fatalf("Gate2 round-trip: %v", err)
	}
	s := string(cfg)
	for _, want := range []string{"app:", "name: demoapp", "server:", "http:", `addr: :18080`, "audit:", "backends:"} {
		if !strings.Contains(s, want) {
			t.Errorf("generated config missing %q:\n%s", want, s)
		}
	}
}

// TestGate2_UnknownKeyRed 键写错生成期即报（回归锁的触发证明：绕过描述符的
// 键路径在 Gate2 爆红）。
func TestGate2_UnknownKeyRed(t *testing.T) {
	bad := map[string]any{
		"app":    map[string]any{"name": "demoapp"},
		"server": map[string]any{"htpp": map[string]any{"addr": ":8080"}}, // 拼写错误
	}
	err := checkContractKeys(bad)
	if err == nil {
		t.Fatal("unknown key must be rejected (Gate2)")
	}
	if !strings.Contains(err.Error(), "server.htpp") {
		t.Errorf("error must name the unknown key path, got: %v", err)
	}
}

// TestGate2_EmptySegmentStaysObject 段存在性是语义：出现但为空的消息段必须
// 序列化为空对象而非 null（null 经 protojson 即「未设置」，段会被装载器丢弃）。
func TestGate2_EmptySegmentStaysObject(t *testing.T) {
	bs := &bootstrapv1.BootstrapConfig{Server: &bootstrapv1.Server{Http: &bootstrapv1.Server_Http{}}}
	tree, err := contractTree(&appspecv1.AppSpec{Meta: &appspecv1.AppMeta{Name: "demo"}}, bs)
	if err != nil {
		t.Fatalf("contractTree: %v", err)
	}
	seg, ok := tree["server"].(map[string]any)["http"].(map[string]any)
	if !ok || seg == nil {
		t.Fatalf("empty http segment must stay an object, got: %#v", tree["server"])
	}
	cfg, err := contractGate(&appspecv1.AppSpec{Meta: &appspecv1.AppMeta{Name: "demo"}}, bs)
	if err != nil {
		t.Fatalf("Gate2: %v", err)
	}
	if !strings.Contains(string(cfg), "http: {}") {
		t.Errorf("empty segment must serialize as {} (not null):\n%s", cfg)
	}
}

// ---------------------------------------------------------------------------
// 字段消费机器守卫（G8 通用解）
// ---------------------------------------------------------------------------

// renderedFixture 把参考夹具走完整树构建（Gate1 → 归一化 → Gate2 → buildTree），
// 拼接全部生成物文本——机器守卫的断言面。
func renderedFixture(t *testing.T) string {
	t.Helper()
	as := guardFixture()
	if err := guardErrs(validateAppSpec(as)); err != nil {
		t.Fatalf("fixture must pass Gate1: %v", err)
	}
	cfg, err := contractGate(as, normalizeBootstrap(as))
	if err != nil {
		t.Fatalf("contractGate: %v", err)
	}
	files, err := buildTree(buildTreeView(as, normalizeBootstrap(as)), cfg)
	if err != nil {
		t.Fatalf("buildTree: %v", err)
	}
	var sb strings.Builder
	for _, f := range files {
		sb.Write(f.Raw)
	}
	return sb.String()
}

// TestMachineGuard_AllFieldsConsumed 正例：遍历 AppSpec 描述符，每个叶子字段
// 都有登记的消费者，且标记真实出现在生成物文本中。
func TestMachineGuard_AllFieldsConsumed(t *testing.T) {
	rendered := renderedFixture(t)
	if errs := checkFieldConsumers(rendered); len(errs) != 0 {
		t.Fatalf("every declared field must have a consumer, got:\n%s", joinErrs(errs))
	}
}

// TestMachineGuard_UnconsumedFieldRed 负例（红）：从守卫表摘除一个消费者登记，
// 守卫必须报「declared but has no consumer」——ServerSpec.gateway 型死声明
// 从此不可能再上线（新增字段忘接消费者 = 测试红）。
func TestMachineGuard_UnconsumedFieldRed(t *testing.T) {
	rendered := renderedFixture(t)
	saved := builderConsumed["bald.appspec.v1.AppSpec.server.gateway"]
	delete(builderConsumed, "bald.appspec.v1.AppSpec.server.gateway")
	defer func() { builderConsumed["bald.appspec.v1.AppSpec.server.gateway"] = saved }()

	errs := checkFieldConsumers(rendered)
	if len(errs) == 0 {
		t.Fatal("field without consumer registration must be reported (test must be red)")
	}
	if !strings.Contains(joinErrs(errs), "no consumer") {
		t.Errorf("unexpected report: %s", joinErrs(errs))
	}
}

// TestMachineGuard_MarkerNotEmittedRed 负例（红）：登记了标记但生成物未消费
// （如 bundle.normalized=true 而 HTTP 面缺席 → bundle.Normalized 不出现），
// 守卫同样报红。
func TestMachineGuard_MarkerNotEmittedRed(t *testing.T) {
	// 无 HTTP 面的声明：bundle.Normalized 无落点（apiserver.go 不 seed）。
	as := guardFixture()
	as.Bootstrap.Server = &bootstrapv1.Server{Grpc: &bootstrapv1.Server_Grpc{Addr: ":19090"}}
	as.Server = &appspecv1.ServerSpec{Grpc: true}
	as.Bundle = &appspecv1.BundleSpec{Normalized: true}
	cfg, err := contractGate(as, normalizeBootstrap(as))
	if err != nil {
		t.Fatalf("contractGate: %v", err)
	}
	files, err := buildTree(buildTreeView(as, normalizeBootstrap(as)), cfg)
	if err != nil {
		t.Fatalf("buildTree: %v", err)
	}
	var sb strings.Builder
	for _, f := range files {
		sb.Write(f.Raw)
	}
	errs := checkFieldConsumers(sb.String())
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "bundle.Normalized(") {
			found = true
		}
	}
	if !found {
		t.Errorf("bundle.normalized consumer marker must be reported missing when no HTTP face is declared, got:\n%s", joinErrs(errs))
	}
}

// ---------------------------------------------------------------------------
// 目标目录树：布局 / 三态归属 / 行数预算
// ---------------------------------------------------------------------------

// genFixtureTree 在临时目录落参考夹具的目录树（--force 语义由调用方控制）。
func genFixtureTree(t *testing.T, dir string, force bool) (*bytes.Buffer, error) {
	t.Helper()
	as := guardFixture()
	if err := guardErrs(validateAppSpec(as)); err != nil {
		t.Fatalf("fixture Gate1: %v", err)
	}
	cfg, err := contractGate(as, normalizeBootstrap(as))
	if err != nil {
		t.Fatalf("contractGate: %v", err)
	}
	files, err := buildTree(buildTreeView(as, normalizeBootstrap(as)), cfg)
	if err != nil {
		t.Fatalf("buildTree: %v", err)
	}
	streams, out := testStreams()
	err = emitTree(dir, files, force, streams)
	return out, err
}

// TestTree_Layout 断言目标目录树的路径集合与归属（D2 §2.1）。
func TestTree_Layout(t *testing.T) {
	dir := t.TempDir()
	out, err := genFixtureTree(t, dir, false)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if n := strings.Count(out.String(), "generated:"); n != 13 {
		t.Errorf("first run must generate 13 files, got %d:\n%s", n, out.String())
	}
	owned := []string{
		filepath.Join("cmd", "demoapp", "main.go"),
		filepath.Join("cmd", "demoapp", "app", "command.go"),
		filepath.Join("internal", "bootstrap", "assemble.gen.go"),
		filepath.Join("internal", "bootstrap", "domains.gen.go"),
		filepath.Join("internal", "apiserver", "domains.gen.go"),
		filepath.Join("internal", "apiserver", "layering_invariant_test.go"),
		filepath.Join("api", "buf.yaml"),
		filepath.Join("api", "buf.gen.yaml"),
		filepath.Join("configs", "demoapp.yaml"),
		"taskfile.yml",
	}
	seeded := []string{
		filepath.Join("internal", "bootstrap", "seed", "seed.go"),
		filepath.Join("internal", "bootstrap", "seed", "policies.go"),
		filepath.Join("internal", "apiserver", "apiserver.go"),
	}
	for _, p := range owned {
		data, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Errorf("owned file %q missing: %v", p, err)
			continue
		}
		if !strings.Contains(string(data), "DO NOT EDIT") && filepath.Ext(p) == ".go" {
			t.Errorf("owned .go file %q must carry DO NOT EDIT header", p)
		}
	}
	for _, p := range seeded {
		data, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Errorf("seeded file %q missing: %v", p, err)
			continue
		}
		if !strings.Contains(string(data), "生成一次") {
			t.Errorf("seeded file %q must be marked 生成一次 (seed-once)", p)
		}
	}
}

// TestTree_LineBudget main.go ≤25 行（D2 §2.1 预算）。
func TestTree_LineBudget(t *testing.T) {
	dir := t.TempDir()
	if _, err := genFixtureTree(t, dir, false); err != nil {
		t.Fatalf("gen: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "cmd", "demoapp", "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	lines := strings.Count(strings.TrimRight(string(data), "\n"), "\n") + 1
	if lines > 25 {
		t.Errorf("cmd/<app>/main.go must stay within 25 lines, got %d:\n%s", lines, data)
	}
}

// TestTree_OwnershipAcrossRegenerations 三态行为（回归生成）：
// 第一次全 generated:；第二次无 --force 在 owned 文件上拒绝（错误带 --force）；
// --force 放行后 owned 覆盖、seeded kept: 且用户改动保留。
func TestTree_OwnershipAcrossRegenerations(t *testing.T) {
	dir := t.TempDir()
	if _, err := genFixtureTree(t, dir, false); err != nil {
		t.Fatalf("first gen: %v", err)
	}
	// 用户手改一个 seeded 文件——重生成必须原样保留。
	seedPath := filepath.Join(dir, "internal", "bootstrap", "seed", "seed.go")
	handEdit := "package seed\n\n// user touched this\n"
	if err := os.WriteFile(seedPath, []byte(handEdit), 0o644); err != nil {
		t.Fatalf("hand-edit seed: %v", err)
	}

	// 第二次：无 --force → owned 拒绝。
	if _, err := genFixtureTree(t, dir, false); err == nil {
		t.Fatal("second gen without --force must fail on owned files")
	} else if !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal must mention --force, got: %v", err)
	}
	// 拒绝路径不得半途改动文件（emitTree 逐文件落盘，首个 owned 拒绝即返回）。
	if got, _ := os.ReadFile(seedPath); string(got) != handEdit {
		t.Errorf("seeded file must survive refused regen, got: %q", got)
	}

	// 第三次：--force → owned 覆盖 + seeded kept:。
	out, err := genFixtureTree(t, dir, true)
	if err != nil {
		t.Fatalf("force regen: %v", err)
	}
	if kept := strings.Count(out.String(), "kept:"); kept != 3 {
		t.Errorf("force regen must report kept: for 3 seeded files, got %d:\n%s", kept, out.String())
	}
	if got, _ := os.ReadFile(seedPath); string(got) != handEdit {
		t.Errorf("seeded file must survive --force regen, got: %q", got)
	}
	mainPath := filepath.Join(dir, "cmd", "demoapp", "main.go")
	before, _ := os.ReadFile(mainPath)
	if _, err := genFixtureTree(t, dir, true); err != nil {
		t.Fatalf("force regen 2: %v", err)
	}
	after, _ := os.ReadFile(mainPath)
	if !bytes.Equal(before, after) {
		t.Error("owned regeneration must be deterministic (byte-identical)")
	}
}

// ---------------------------------------------------------------------------
// 真编译 + 冒烟（沿用既有设施：临时 module 脚手架 + go build + 起服务）
// ---------------------------------------------------------------------------

// scaffoldModule 在 dir 落一个与 _example/bald 同构的消费者 module：
// go.mod 逐行改写（module 行换名、replace 相对路径转绝对），go.sum 原样复制
// ——依赖闭包与 _example 完全一致，离线可构建。
func scaffoldModule(t *testing.T, dir, module string) {
	t.Helper()
	base := exampleModule(t)
	gomod, err := os.ReadFile(filepath.Join(base, "go.mod"))
	if err != nil {
		t.Fatalf("read example go.mod: %v", err)
	}
	lines := strings.Split(string(gomod), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "module "):
			lines[i] = "module " + module
		case strings.HasPrefix(trimmed, "replace "):
			parts := strings.SplitN(trimmed, "=>", 2)
			if len(parts) == 2 {
				lines[i] = parts[0] + "=> " + filepath.Join(base, strings.TrimSpace(parts[1]))
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	gosum, err := os.ReadFile(filepath.Join(base, "go.sum"))
	if err != nil {
		t.Fatalf("read example go.sum: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), gosum, 0o644); err != nil {
		t.Fatalf("write go.sum: %v", err)
	}
}

// seedDomainPackage 在临时 module 落一个最小域包（W2 目标形态的域声明值；
// gen domain / W3b 的 seed 职责，测试脚手架代为承担）。
func seedDomainPackage(t *testing.T, dir, module string) {
	t.Helper()
	pkgDir := filepath.Join(dir, "internal", "apiserver", "greetv1")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir domain pkg: %v", err)
	}
	src := `// Package greetv1 是最小测试域包（W2 目标形态：包导出一个 appkit.Domain 值）。
package greetv1

import (
	"github.com/kalandramo/bald/pkg/appkit"
)

// Domain 是 greet 域的声明值（变参直传 appkit.MustServe）。
var Domain = appkit.Domain{Name: "greet"}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "greetv1.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write domain pkg: %v", err)
	}
}

// writeSpecJSON 把 AppSpec 结构写为 protojson 文件（gen app --spec 的输入）。
func writeSpecJSON(t *testing.T, dir string, as *appspecv1.AppSpec) string {
	t.Helper()
	opts := protojson.MarshalOptions{Indent: "  "}
	data, err := opts.Marshal(as)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	path := filepath.Join(dir, "appspec.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	return path
}

// runGenAppInTemp 把参考夹具生成到临时 module（复用 runGenAppSpec 全流水线：
// 三道门 + 三态落盘），返回二进制路径。
func runGenAppInTemp(t *testing.T, as *appspecv1.AppSpec, withDomain bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	scaffoldModule(t, dir, as.GetMeta().GetModule())
	if withDomain {
		seedDomainPackage(t, dir, as.GetMeta().GetModule())
	}
	specPath := writeSpecJSON(t, dir, as)
	t.Chdir(dir) // gen app 的产出根 = CWD（CLI 语义）；相对树必须落在临时 module 内
	cmd := genAppCmd(NewDefaultIOStreams())
	cmd.SetArgs([]string{"--spec", specPath, "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gen app --spec: %v", err)
	}
	return dir, specPath
}

// TestTree_GeneratedProjectCompiles 生成的工程在独立消费者 module 内真实
// go build 通过（http+grpc / full 网关 / 纯域清单三形态）。
func TestTree_GeneratedProjectCompiles(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}
	full := guardFixture()
	full.Bootstrap.Server.Http.Driver = "grpc-gateway"
	full.Server.Gateway = true
	cases := map[string]*appspecv1.AppSpec{
		"httpgrpc": guardFixture(),
		"fullgateway": full,
		"domainsonly": {
			Meta: &appspecv1.AppMeta{Name: "demodomain", Module: "demo.example/app", Desc: "d"},
			Domains: []*appspecv1.DomainSpec{{
				Name: "greet", ProtoPackage: "greet.v1", Service: "greet.v1.GreetService",
				Model: "internal/apiserver/model/greet.go", GoPackage: "demo.example/app/internal/apiserver/greetv1",
			}},
		},
	}
	for name, as := range cases {
		as := as
		t.Run(name, func(t *testing.T) {
			dir, _ := runGenAppInTemp(t, as, true)
			build := exec.Command("go", "build", "./...")
			build.Dir = dir
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("generated project does not compile: %v\n%s", err, out)
			}
		})
	}
}

// TestTree_GeneratedProjectRuns 冒烟：编译并真实运行生成的工程（无传输面，
// Run 阻塞在信号等待），走 MustServe 全链路（--config 解析、契约装载、
// StrictContract、std Provider 装配）——编译过但启动即崩会被本测试捕获。
func TestTree_GeneratedProjectRuns(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}
	as := &appspecv1.AppSpec{
		Meta: &appspecv1.AppMeta{Name: "smoketree", Module: "demo.example/app", Desc: "smoke"},
	}
	dir, _ := runGenAppInTemp(t, as, false)
	bin := filepath.Join(dir, "smoketree.bin")
	build := exec.Command("go", "build", "-o", bin, "./cmd/smoketree")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin)
	cmd.Dir = dir // configs/smoketree.yaml 相对运行目录
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start generated app: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("generated app exited prematurely (err=%v):\n%s", err, out.String())
	case <-time.After(3 * time.Second):
		// 3s 仍存活：装配正确，Run 正常阻塞在信号等待。
	}
	_ = cmd.Process.Kill()
	<-done
}

// joinErrs 聚合错误列表为可断言文本。
func joinErrs(errs []error) string {
	var sb strings.Builder
	for _, e := range errs {
		sb.WriteString(e.Error())
		sb.WriteString("\n")
	}
	return sb.String()
}