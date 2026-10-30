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

// TestGate1_Variants 变体声明：name 必填/形状/唯一，build_tags 非空且逐项
// 非空、无首尾空白、不重复；合法声明通过。
func TestGate1_Variants(t *testing.T) {
	valid := guardFixture()
	valid.Variants = []*appspecv1.Variant{{
		Name: "grpcgw", BuildTags: []string{"grpcgw"}, Description: "d",
	}, {
		Name: "linux_amd64", BuildTags: []string{"linux", "amd64", "go1.27"},
	}}
	if errs := validateAppSpec(valid); len(errs) != 0 {
		t.Fatalf("valid variants must pass Gate1: %v", errs)
	}

	cases := map[string]struct {
		variants []*appspecv1.Variant
		want     string
	}{
		"empty name": {
			[]*appspecv1.Variant{{BuildTags: []string{"grpcgw"}}},
			`variants[0].name is required`,
		},
		"invalid name": {
			[]*appspecv1.Variant{{Name: "GrpcGW", BuildTags: []string{"grpcgw"}}},
			`variants[0].name "GrpcGW" is invalid`,
		},
		"duplicate name": {
			[]*appspecv1.Variant{
				{Name: "grpcgw", BuildTags: []string{"grpcgw"}},
				{Name: "grpcgw", BuildTags: []string{"nacos"}},
			},
			`variants[1].name "grpcgw" is declared twice`,
		},
		"empty build_tags": {
			[]*appspecv1.Variant{{Name: "grpcgw"}},
			`variants[0].build_tags is empty`,
		},
		"blank tag": {
			[]*appspecv1.Variant{{Name: "grpcgw", BuildTags: []string{"  "}}},
			`variants[0].build_tags[0] is empty`,
		},
		"whitespace tag": {
			[]*appspecv1.Variant{{Name: "grpcgw", BuildTags: []string{" grpcgw"}}},
			`has surrounding whitespace`,
		},
		"duplicate tag": {
			[]*appspecv1.Variant{{Name: "grpcgw", BuildTags: []string{"grpcgw", "grpcgw"}}},
			`variants[0].build_tags[1] "grpcgw" is duplicated`,
		},
		"comma expression": {
			[]*appspecv1.Variant{{Name: "grpcgw", BuildTags: []string{"grpcgw,nacos"}}},
			`"grpcgw,nacos" is not a single build tag`,
		},
		"negation expression": {
			[]*appspecv1.Variant{{Name: "grpcgw", BuildTags: []string{"!nacos"}}},
			`"!nacos" is not a single build tag`,
		},
		"conjunction expression": {
			[]*appspecv1.Variant{{Name: "grpcgw", BuildTags: []string{"grpcgw&&nacos"}}},
			`"grpcgw&&nacos" is not a single build tag`,
		},
	}
	for name, tc := range cases {
		as := guardFixture()
		as.Variants = tc.variants
		msg := joinErrs(validateAppSpec(as))
		if !strings.Contains(msg, tc.want) {
			t.Errorf("%s: error %q missing %q", name, msg, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 快捷方式归一化（bootstrap 为空时的兼容通道）
// ---------------------------------------------------------------------------

// TestNormalizeBootstrap bootstrap 为空时，旧 6 字段快捷方式归一化为契约投影；
// bootstrap 非空时原样透传（一致性已由 Gate1 把关）。
func TestNormalizeBootstrap(t *testing.T) {
	as := &appspecv1.AppSpec{
		Meta:          &appspecv1.AppMeta{Name: "legacy"},
		Server:        &appspecv1.ServerSpec{Http: true, HttpAddr: ":18080", Grpc: true, Gateway: true},
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

// variantFixture 挂上机器守卫与生成测试共用的变体声明：取值与
// fieldConsumers 的 variants 标记一一同构（改一处必须同步另一处）。
func variantFixture(as *appspecv1.AppSpec) *appspecv1.AppSpec {
	as.Variants = []*appspecv1.Variant{{
		Name: "grpcgw", BuildTags: []string{"grpcgw"}, Description: "gateway wiring behind the grpcgw tag",
	}}
	return as
}

// renderedFixture 把参考夹具走完整树构建（Gate1 → 归一化 → Gate2 → buildTree），
// 拼接全部生成物文本——机器守卫的断言面。变体字段的消费者只在声明了
// variants 时才落盘（variants.gen.go / assemble.gen.go 的调用），所以追加
// 一份带 variants 的渲染，让守卫能按标记核对这三个字段。
func renderedFixture(t *testing.T) string {
	t.Helper()
	return renderSpecForGuard(t, guardFixture()) + renderSpecForGuard(t, variantFixture(guardFixture()))
}

// renderSpecForGuard 是 renderedFixture 的单份渲染。
func renderSpecForGuard(t *testing.T, as *appspecv1.AppSpec) string {
	t.Helper()
	if err := guardErrs(validateAppSpec(as)); err != nil {
		t.Fatalf("fixture must pass Gate1: %v", err)
	}
	cfg, err := contractGate(as, normalizeBootstrap(as))
	if err != nil {
		t.Fatalf("contractGate: %v", err)
	}
	files, err := buildTree(buildTreeView(as, normalizeBootstrap(as)), map[string][]byte{"": cfg})
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
	files, err := buildTree(buildTreeView(as, normalizeBootstrap(as)), map[string][]byte{"": cfg})
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

// genFixtureTree 在临时目录落参考夹具的目录树（--adopt 语义由调用方控制）。
func genFixtureTree(t *testing.T, dir string, adopt bool) (*bytes.Buffer, error) {
	t.Helper()
	as := guardFixture()
	if err := guardErrs(validateAppSpec(as)); err != nil {
		t.Fatalf("fixture Gate1: %v", err)
	}
	cfg, err := contractGate(as, normalizeBootstrap(as))
	if err != nil {
		t.Fatalf("contractGate: %v", err)
	}
	files, err := buildTree(buildTreeView(as, normalizeBootstrap(as)), map[string][]byte{"": cfg})
	if err != nil {
		t.Fatalf("buildTree: %v", err)
	}
	streams, out := testStreams()
	err = emitTree(dir, files, adopt, streams)
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
// 第一次全 generated:；第二次无 --adopt 在 owned 文件上拒绝（错误给出两种
// 放行开关）；--adopt 放行后 owned 覆盖、seeded kept: 且用户改动保留。
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

	// 第二次：无 --adopt → owned 拒绝。
	if _, err := genFixtureTree(t, dir, false); err == nil {
		t.Fatal("second gen without --adopt must fail on owned files")
	} else if !strings.Contains(err.Error(), "--adopt") {
		t.Errorf("refusal must mention --adopt, got: %v", err)
	}
	// 拒绝路径不得半途改动文件（emitTree 逐文件落盘，首个 owned 拒绝即返回）。
	if got, _ := os.ReadFile(seedPath); string(got) != handEdit {
		t.Errorf("seeded file must survive refused regen, got: %q", got)
	}

	// 第三次：--adopt → owned 覆盖 + seeded kept:。
	out, err := genFixtureTree(t, dir, true)
	if err != nil {
		t.Fatalf("adopt regen: %v", err)
	}
	// seed.go、seed/policies.go、apiserver.go、apiserver/domains.gen.go（预域
	// 形态由 gen domain 升级，故 seed 而非 owned）。
	if kept := strings.Count(out.String(), "kept:"); kept != 4 {
		t.Errorf("adopt regen must report kept: for 4 seeded files, got %d:\n%s", kept, out.String())
	}
	if got, _ := os.ReadFile(seedPath); string(got) != handEdit {
		t.Errorf("seeded file must survive --adopt regen, got: %q", got)
	}
	mainPath := filepath.Join(dir, "cmd", "demoapp", "main.go")
	before, _ := os.ReadFile(mainPath)
	if _, err := genFixtureTree(t, dir, true); err != nil {
		t.Fatalf("adopt regen 2: %v", err)
	}
	after, _ := os.ReadFile(mainPath)
	if !bytes.Equal(before, after) {
		t.Error("owned regeneration must be deterministic (byte-identical)")
	}
}

// TestGenApp_VariantsProjectedToPlan Q7：AppSpec.variants 落
// internal/bootstrap/variants.gen.go，声明**恒**进计划（不带 tag 也能审计
// 声明面），命中的变体另由 appkit 产 ambient 诊断（生成期不展开 tag 组合、
// 不按 tag 分文件——同一组 tag 命中多个变体也只多一条诊断，不会重复定义
// 符号）；未声明 variants 的树不落该文件、assemble.gen.go 也不出现调用。
func TestGenApp_VariantsProjectedToPlan(t *testing.T) {
	as := guardFixture()
	as.Variants = []*appspecv1.Variant{
		{Name: "grpcgw", BuildTags: []string{"grpcgw"}, Description: "gateway registration injected by -tags grpcgw"},
		{Name: "nacos_config", BuildTags: []string{"nacos", "config"}},
	}
	dir, specPath := runGenAppInTemp(t, as)

	raw, err := os.ReadFile(filepath.Join(dir, "internal", "bootstrap", "variants.gen.go"))
	if err != nil {
		t.Fatalf("read variants.gen.go: %v", err)
	}
	src := string(raw)
	for _, want := range []string{
		"package bootstrap",
		"func variantOptions() []appkit.Option {",
		"tags := appkit.BuildTags()",
		"return []appkit.Option{appkit.WithVariantDecls(",
		`Name:      "grpcgw",`,
		`BuildTags: []string{"grpcgw"},`,
		`Detail:    "gateway registration injected by -tags grpcgw",`,
		`Active:    appkit.HasBuildTags(tags, "grpcgw"),`,
		`Name:      "nacos_config",`,
		`BuildTags: []string{"nacos", "config"},`,
		`Detail:    "build-tag variant declared in AppSpec.variants (tags: nacos,config)",`,
		`Active:    appkit.HasBuildTags(tags, "nacos", "config"),`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("variants.gen.go missing %q:\n%s", want, src)
		}
	}

	assemble, err := os.ReadFile(filepath.Join(dir, "internal", "bootstrap", "assemble.gen.go"))
	if err != nil {
		t.Fatalf("read assemble.gen.go: %v", err)
	}
	if !strings.Contains(string(assemble), "opts = append(opts, variantOptions()...)") {
		t.Errorf("assemble.gen.go must wire variantOptions:\n%s", assemble)
	}

	// 变体投影是 owned 产物：重跑 --check 必须零差异。
	cmd := genAppCmd(NewDefaultIOStreams())
	cmd.SetArgs([]string{"--spec", specPath, "--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gen app --check after variants: %v", err)
	}

	plainDir := t.TempDir()
	if _, err := genFixtureTree(t, plainDir, false); err != nil {
		t.Fatalf("gen plain tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(plainDir, "internal", "bootstrap", "variants.gen.go")); !os.IsNotExist(err) {
		t.Errorf("a tree without AppSpec.variants must not emit variants.gen.go: %v", err)
	}
	plainAssemble, err := os.ReadFile(filepath.Join(plainDir, "internal", "bootstrap", "assemble.gen.go"))
	if err != nil {
		t.Fatalf("read plain assemble.gen.go: %v", err)
	}
	if strings.Contains(string(plainAssemble), "variantOptions") {
		t.Errorf("a tree without AppSpec.variants must not reference variantOptions:\n%s", plainAssemble)
	}
}

// TestGenApp_VariantsPrunedWhenUndeclared 声明移除后旧产物必须退场：变体
// 列表清空（或整个 variants 字段消失）时，--adopt 要删掉上一轮的
// variants.gen.go，assemble.gen.go 也不再引用 variantOptions——owned 文件
// 的归属由当前声明重算，不认历史写入者。
func TestGenApp_VariantsPrunedWhenUndeclared(t *testing.T) {
	dir := t.TempDir()
	scaffoldModule(t, dir, "demo.example/app")
	specPath := writeSpecJSON(t, dir, variantFixture(guardFixture()))
	t.Chdir(dir)
	gen := func() {
		cmd := genAppCmd(NewDefaultIOStreams())
		cmd.SetArgs([]string{"--spec", specPath, "--adopt"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("gen app: %v", err)
		}
	}
	variantsPath := filepath.Join(dir, "internal", "bootstrap", "variants.gen.go")
	gen()
	if _, err := os.Stat(variantsPath); err != nil {
		t.Fatalf("declared variants must emit variants.gen.go: %v", err)
	}

	writeSpecJSON(t, dir, guardFixture())
	gen()
	if _, err := os.Stat(variantsPath); !os.IsNotExist(err) {
		t.Errorf("dropping AppSpec.variants must prune the stale owned file: %v", err)
	}
	assemble, err := os.ReadFile(filepath.Join(dir, "internal", "bootstrap", "assemble.gen.go"))
	if err != nil {
		t.Fatalf("read assemble.gen.go: %v", err)
	}
	if strings.Contains(string(assemble), "variantOptions") {
		t.Errorf("assemble.gen.go must drop the call after variants are undeclared:\n%s", assemble)
	}
}

// TestTree_VariantsProjectedAtRuntime 运行期口径：同一份生成树，声明**恒**
// 出现在 doctor 的 variants 段（不带 tag 也能审计声明面），但只有带 -tags
// 构建的二进制把变体标为 active 并产 variant:<name> 诊断——命中的唯一依据
// 是这个二进制实际怎么编的，不是生成期猜的 tag 组合。
func TestTree_VariantsProjectedAtRuntime(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}
	dir, _ := runGenAppInTemp(t, variantFixture(guardFixture()))
	for _, tc := range []struct {
		name string
		tags []string
		want bool
	}{
		{"tagged", []string{"grpcgw"}, true},
		{"untagged", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "demoapp")
			args := []string{"build"}
			if len(tc.tags) > 0 {
				args = append(args, "-tags", strings.Join(tc.tags, ","))
			}
			args = append(args, "-o", bin, "./cmd/demoapp")
			build := exec.Command("go", args...)
			build.Dir = dir
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build %v: %v\n%s", tc.tags, err, out)
			}
			doctor := exec.Command(bin, "doctor", "--config", "configs/demoapp.yaml")
			doctor.Dir = dir
			out, err := doctor.CombinedOutput()
			if err != nil {
				t.Fatalf("doctor: %v\n%s", err, out)
			}
			has := strings.Contains(string(out), "ambient [variant:grpcgw]")
			if has != tc.want {
				t.Errorf("variant:grpcgw diagnostic present = %v, want %v:\n%s", has, tc.want, out)
			}
			if tagsLine := strings.Contains(string(out), "build tags: grpcgw"); tagsLine != tc.want {
				t.Errorf("build tags line mismatch (got %v, want %v):\n%s", tagsLine, tc.want, out)
			}
			// 声明面恒可见：变体总数与声明名不随构建变化，只有 active 计数变。
			active := "0"
			if tc.want {
				active = "1"
			}
			if !strings.Contains(string(out), "variants: 1 (active "+active+")") {
				t.Errorf("variants section must report 1 declared, active=%v:\n%s", tc.want, out)
			}
			if !strings.Contains(string(out), "grpcgw") {
				t.Errorf("declared variant name must be listed in doctor:\n%s", out)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 真编译 + 冒烟（沿用既有设施：临时 module 脚手架 + go build + 起服务）
// ---------------------------------------------------------------------------

// scaffoldModule 在 dir 落一个与 _example/bald 同构的消费者 module：
// go.mod 逐行改写（module 行换名、replace 相对路径转绝对），go.sum 原样复制
// ——依赖闭包与 _example 完全一致，离线可构建。
//
// hermetic 化（Linux CI 修复）：_example/bald/go.mod 的 kratos fork replace
// （../../../kratos/...）指向 bald 仓库的**兄弟目录** checkout——只在本机
// 存在，GitHub runner 只有仓库本身（checkout 在 <workspace>/bald，兄弟目录
// 解析到 <workspace>/kratos，不存在），go mod tidy 加载模块图即炸。这是
// TestTree_* 全红的唯一直因（「reading
// .../kratos/contrib/config/nacos/go.mod: no such file or directory」）。
// 处置：replace 目标缺 go.mod 的整行剔除，并连带剔除该模块的零时间占位版本
// require（v?…-00010101000000-000000000000 只可能经 replace 满足，留 require
// 去 replace 在 proxy 上必 404）。剔除判据 = 目标缺 go.mod **或逃出仓库
// checkout**（与 TestScaffoldModule_ReplacesAreHermetic 同一不变量——只判
// 存在性会漏掉本机恰有兄弟 checkout 的机器）。生成树不 import kratos
// contrib fork（夹具无 registry/nacos 配置，本仓 registry/nacos 也只依赖
// 真版本 kratos v3.0.0），剔除不改变被测生成物的依赖闭包；_example/bald
// 本体（用户面）不动。
func scaffoldModule(t *testing.T, dir, module string) {
	t.Helper()
	base := exampleModule(t)
	gomod, err := os.ReadFile(filepath.Join(base, "go.mod"))
	if err != nil {
		t.Fatalf("read example go.mod: %v", err)
	}
	lines := strings.Split(string(gomod), "\n")
	// 第一遍：module 换名、replace 目标转绝对；目标缺 go.mod 或逃出仓库
	// checkout 的记入 dropped，行体置删除标记。
	const drop = "\x00drop"
	var dropped = map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "module "):
			lines[i] = "module " + module
		case strings.HasPrefix(trimmed, "replace "):
			parts := strings.SplitN(trimmed, "=>", 2)
			if len(parts) != 2 {
				continue
			}
			fields := strings.Fields(parts[0])
			if len(fields) < 2 {
				continue
			}
			target := filepath.Join(base, strings.TrimSpace(parts[1]))
			// hermetic 判据与 TestScaffoldModule_ReplacesAreHermetic 同一不变量：
			// 目标必须仍在仓库 checkout 内（根从 base 派生，不依赖进程 cwd）且
			// 自带 go.mod。只判存在性不够——本机恰有 kratos 兄弟 checkout 时
			// 外指 replace 在本机成立、runner 上必炸。
			root := filepath.Clean(filepath.Join(base, "..", ".."))
			inside := false
			if rel, relErr := filepath.Rel(root, target); relErr == nil &&
				rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				inside = true
			}
			if _, err := os.Stat(filepath.Join(target, "go.mod")); err != nil || !inside {
				dropped[fields[1]] = true
				lines[i] = drop
				continue
			}
			lines[i] = parts[0] + "=> " + target
		}
	}
	// 第二遍：剔除删除标记行 + 已删 replace 模块的占位版本 require
	// （require ( 块内条目与单行 require 都处理）。
	filtered := lines[:0]
	inRequire := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == drop:
			continue
		case strings.HasPrefix(trimmed, "require ("):
			inRequire = true
		case inRequire && trimmed == ")":
			inRequire = false
		case len(dropped) > 0 && (inRequire || strings.HasPrefix(trimmed, "require ")):
			fields := strings.Fields(strings.TrimPrefix(trimmed, "require "))
			if len(fields) >= 2 && dropped[fields[0]] &&
				strings.HasSuffix(fields[1], "-00010101000000-000000000000") {
				continue
			}
		}
		filtered = append(filtered, line)
	}
	lines = filtered
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

// TestScaffoldModule_ReplacesAreHermetic 把 Linux CI 的失败模式钉成全平台可跑
// 的不变量（不依赖 runner 也能验）：脚手架 go.mod 的每个 replace 目标必须
// 落在仓库 checkout 内且自带 go.mod——runner 上只有仓库本身，指向兄弟目录
// 的 replace 只在本机碰巧成立（tidy 全红的根因）；每个
// 零时间占位版本 require 必须有 replace 兜底（占位伪版本 proxy 上不存在）。
func TestScaffoldModule_ReplacesAreHermetic(t *testing.T) {
	dir := t.TempDir()
	scaffoldModule(t, dir, "demo.example/hermetic")
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatalf("read scaffolded go.mod: %v", err)
	}
	root := repoRoot(t)
	replaced := map[string]bool{}
	isInsideRepo := func(target string) (bool, error) {
		rel, err := filepath.Rel(root, target)
		if err != nil {
			return false, err
		}
		return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "replace ") {
			continue
		}
		parts := strings.SplitN(trimmed, "=>", 2)
		if len(parts) != 2 {
			t.Errorf("malformed replace line: %q", trimmed)
			continue
		}
		fields := strings.Fields(parts[0])
		if len(fields) < 2 {
			t.Errorf("malformed replace line: %q", trimmed)
			continue
		}
		mod, target := fields[1], strings.TrimSpace(parts[1])
		if !filepath.IsAbs(target) {
			t.Errorf("replace %s target must be absolute, got %q", mod, target)
			continue
		}
		if _, err := os.Stat(filepath.Join(target, "go.mod")); err != nil {
			t.Errorf("replace %s target %s must contain go.mod: %v", mod, target, err)
			continue
		}
		if inside, err := isInsideRepo(target); err != nil || !inside {
			t.Errorf("replace %s target %s escapes the repo checkout %s (rel=%q, err=%v): a sibling-of-repo replace only resolves on machines with that sibling present", mod, target, root, rel2s(root, target), err)
			continue
		}
		replaced[mod] = true
	}
	inRequire := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "require ("):
			inRequire = true
			continue
		case inRequire && trimmed == ")":
			inRequire = false
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(trimmed, "require "))
		if len(fields) < 2 || !strings.HasPrefix(fields[1], "v") {
			continue
		}
		if strings.HasSuffix(fields[1], "-00010101000000-000000000000") && !replaced[fields[0]] {
			t.Errorf("placeholder-version require %s %s has no replace: go mod tidy would try the zero-time pseudo-version on the proxy and 404", fields[0], fields[1])
		}
	}
}

// rel2s 是守卫报错用的相对化捷径（失败时退回原样，不影响断言）。
func rel2s(root, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return target
	}
	return rel
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
// 三道门 + 三态落盘），并在声明域存在时跟进 gen domain——域产物
// （域包声明值骨架 + 清单扇出 + 逐域 handler）由 gen domain 落地，测试不再
// 自备域包脚手架。返回 module 目录与 spec 路径。
func runGenAppInTemp(t *testing.T, as *appspecv1.AppSpec) (string, string) {
	t.Helper()
	dir := t.TempDir()
	scaffoldModule(t, dir, as.GetMeta().GetModule())
	specPath := writeSpecJSON(t, dir, as)
	t.Chdir(dir) // gen app 的产出根 = CWD（CLI 语义）；相对树必须落在临时 module 内
	cmd := genAppCmd(NewDefaultIOStreams())
	cmd.SetArgs([]string{"--spec", specPath, "--adopt"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gen app --spec: %v", err)
	}
	if len(as.GetDomains()) > 0 {
		writeFixtureProto(t, dir) // gen domain 的 service 校验与注解派生以真实 proto 为输入
		dcmd := genDomainCmd(NewDefaultIOStreams())
		dcmd.SetArgs([]string{"--spec", specPath, "--adopt"})
		if err := dcmd.Execute(); err != nil {
			t.Fatalf("gen domain: %v", err)
		}
	}
	return dir, specPath
}

// writeFixtureProto 在临时 module 落一个与 guardFixture 声明同构的最小域
// proto（package greet.v1 + GreetService + google.api.http 注解）：service
// 全名与 guardFixture.domains[0].service 对账；REST 资源段用复数 greets，
// 使 gRPC 面（greet）与 HTTP 面（greets）归一化结果不同——授权别名表的
// 非空路径由此得到真实输入。
func writeFixtureProto(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "api", "proto", "greet", "v1", "greet.proto")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir proto dir: %v", err)
	}
	src := `syntax = "proto3";

package greet.v1;

import "google/api/annotations.proto";

option go_package = "demo.example/app/api/gen/go/greet/v1;greetv1";

// GreetService 是测试夹具域 service（与 guardFixture 的 domains[0].service 对账）。
service GreetService {
  rpc Greet(GreetRequest) returns (GreetResponse) {
    option (google.api.http) = {
      post: "/v1/greets"
      body: "*"
    };
  }

  rpc GreetGet(GreetGetRequest) returns (GreetResponse) {
    option (google.api.http) = {
      get: "/v1/greets/{name}"
    };
  }
}

message GreetRequest {
  string name = 1;
}

message GreetGetRequest {
  string name = 1;
}

message GreetResponse {
  string greet = 1;
}
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture proto: %v", err)
	}
	// buf generate 的等价物：绑定文件默认生成，import 这里的消息类型——
	// 桩必须满足 proto.Message（web.BindProtoRequest/WriteProtoResponse 的
	// 签名要求），所以用 emptypb.Empty 的类型别名（真实流水线由 protoc-gen-go
	// 产出完整实现；这里只要「存在且可编译」）。
	pkgDir := filepath.Join(dir, "api", "gen", "go", "greet", "v1")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir proto go package: %v", err)
	}
	stub := `package greetv1

import "google.golang.org/protobuf/types/known/emptypb"

// 测试桩：真实产物由 protoc-gen-go 生成；别名只为让生成绑定可编译。
type GreetRequest = emptypb.Empty

type GreetGetRequest = emptypb.Empty

type GreetResponse = emptypb.Empty
`
	if err := os.WriteFile(filepath.Join(pkgDir, "greet.pb.go"), []byte(stub), 0o644); err != nil {
		t.Fatalf("write proto go stub: %v", err)
	}
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
	withVariants := guardFixture()
	withVariants.Variants = []*appspecv1.Variant{{
		Name: "grpcgw", BuildTags: []string{"grpcgw"}, Description: "d",
	}}
	cases := map[string]*appspecv1.AppSpec{
		"httpgrpc":    guardFixture(),
		"variants":    withVariants,
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
			dir, _ := runGenAppInTemp(t, as)
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
	dir, _ := runGenAppInTemp(t, as)
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
