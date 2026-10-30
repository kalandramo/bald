// variant_test.go 钉住 Q7 的计划投影口径：变体声明恒进 Plan.Variants（未命中
// 也列出，供 --plan/doctor 审计声明面），命中由 appkit 按二进制实际的构建
// 约束求值（go/build/constraint）并把声明增量（Effects）并入装配；声明面
// 本身的两类错误（缺 Name、重复 Name）与条件形式错误必须在计划期拒绝。
package appkit

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kalandramo/bald/bconf"
)

// setTestBuildTags 注入构建期 tag 集：命中判据必须与 go build 同源，测试
// 不能靠改环境变量伪造 -tags。
func setTestBuildTags(t *testing.T, tags ...string) {
	t.Helper()
	old := testBuildTagSet
	testBuildTagSet = func() map[string]bool {
		m := make(map[string]bool, len(tags))
		for _, tag := range tags {
			m[tag] = true
		}
		return m
	}
	t.Cleanup(func() { testBuildTagSet = old })
}

func TestVariantProjection_DeclarationsAlwaysInPlan(t *testing.T) {
	setTestBuildTags(t, "grpcgw")
	cfgPath := writePlanConfig(t, "variant-app", "")
	p, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithVariantDecls(
			VariantDecl{Name: "nacos_config", When: "nacos && config"},
			VariantDecl{Name: "grpcgw", When: "grpcgw && !nacos", Detail: "gateway wiring", Active: true},
		),
	)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(p.Variants) != 2 {
		t.Fatalf("declared variants must all reach the plan, got %d: %+v", len(p.Variants), p.Variants)
	}
	// 顺序与声明无关（置换不变）：Plan 按 Name 排序。
	if p.Variants[0].Name != "grpcgw" || p.Variants[1].Name != "nacos_config" {
		t.Fatalf("variants must be sorted by name, got: %+v", p.Variants)
	}
	// Active 是求值结果：输入的 Active 被忽略，判据只看 When 与二进制 tag。
	if !p.Variants[0].Active || p.Variants[1].Active {
		t.Errorf("active flags must come from constraint evaluation: %+v", p.Variants)
	}
	if p.Variants[0].Detail != "gateway wiring" {
		t.Errorf("declared detail must be kept, got %q", p.Variants[0].Detail)
	}
	if p.Variants[1].Detail == "" || !strings.Contains(p.Variants[1].Detail, "nacos && config") {
		t.Errorf("a variant without description must get a default detail naming its condition, got %q", p.Variants[1].Detail)
	}

	// I11：已声明的变体不进 ambient——增量在节点/路由上带来源，不需要旁白。
	for _, w := range p.Warnings {
		if w.Code == "ambient" && strings.HasPrefix(w.Subject, "variant:") {
			t.Errorf("declared variants must not emit ambient diagnostics, got %+v", w)
		}
	}

	var explain bytes.Buffer
	p.Explain(&explain)
	for _, want := range []string{
		"variants: 2 (active 1)",
		"active   grpcgw",
		"declared nacos_config",
		"when=grpcgw && !nacos",
		"when=nacos && config",
	} {
		if !strings.Contains(explain.String(), want) {
			t.Errorf("plan explain missing %q:\n%s", want, explain.String())
		}
	}

	var doctor bytes.Buffer
	writeDoctor(&doctor, p, nil, nil, domainReport{})
	for _, want := range []string{
		"variants: 2 (active 1)",
		"active   grpcgw",
		"declared nacos_config",
		"when=grpcgw && !nacos",
		"when=nacos && config",
	} {
		if !strings.Contains(doctor.String(), want) {
			t.Errorf("doctor output missing %q:\n%s", want, doctor.String())
		}
	}
}

// BuildTags 是 When 的迁移糖：各项 && 连接后与 When 同义；两者同时声明是
// 声明面矛盾，必须在计划期失败而不是择一。
func TestVariantProjection_BuildTagsSugar(t *testing.T) {
	setTestBuildTags(t, "nacos", "config")
	cfgPath := writePlanConfig(t, "variant-sugar", "")
	p, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithVariantDecls(VariantDecl{Name: "nacos_config", BuildTags: []string{"nacos", "config"}}),
	)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(p.Variants) != 1 || p.Variants[0].When != "nacos && config" || !p.Variants[0].Active {
		t.Fatalf("BuildTags must canonicalize to a && expression and evaluate: %+v", p.Variants)
	}

	if _, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithVariantDecls(VariantDecl{Name: "x", When: "nacos", BuildTags: []string{"nacos"}}),
	); err == nil || !strings.Contains(err.Error(), "both When and BuildTags") {
		t.Fatalf("When + BuildTags must fail the plan, got %v", err)
	}
}

// 无条件声明（When 空，仅手写可达）恒命中；I11 下它同样不进 ambient。
func TestVariantProjection_Unconditional(t *testing.T) {
	cfgPath := writePlanConfig(t, "variant-always", "")
	p, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithVariantDecls(VariantDecl{Name: "always", Detail: "hand-written always-on wiring"}),
	)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !p.Variants[0].Active || p.Variants[0].When != "" {
		t.Fatalf("empty When must be unconditional and active: %+v", p.Variants[0])
	}
	for _, w := range p.Warnings {
		if w.Code == "ambient" && w.Subject == "variant:always" {
			t.Fatalf("unconditional variant must not emit ambient (I11): %+v", w)
		}
	}
}

// I11：已声明变体与手写 ambient 不能描述同一身份——声明增量与注入说明两套
// 口径并存会让计划自相矛盾，迁移期为硬错误（删掉 WithAmbient 即可）。
func TestVariantProjection_RejectsAmbientDuplicate(t *testing.T) {
	setTestBuildTags(t, "grpcgw")
	cfgPath := writePlanConfig(t, "variant-ambient", "")
	_, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithVariantDecls(VariantDecl{Name: "grpcgw", When: "grpcgw", Detail: "from the spec"}),
		WithAmbient(Ambient{Name: "variant:grpcgw", Detail: "from init()"}),
	)
	if err == nil || !strings.Contains(err.Error(), "remove the WithAmbient") {
		t.Fatalf("declared variant + ambient duplicate must fail the plan, got %v", err)
	}
}

func TestVariantProjection_RejectsBadDeclarations(t *testing.T) {
	cfgPath := writePlanConfig(t, "variant-bad", "")
	for _, tc := range []struct {
		name string
		vs   []VariantDecl
		want string
	}{
		{
			name: "missing name",
			vs:   []VariantDecl{{When: "grpcgw"}},
			want: "requires a Name",
		},
		{
			name: "duplicate name",
			vs:   []VariantDecl{{Name: "grpcgw", When: "grpcgw"}, {Name: "grpcgw", When: "nacos"}},
			want: "declared twice",
		},
		{
			name: "invalid expression",
			vs:   []VariantDecl{{Name: "broken", When: "a &&"}},
			want: "build constraint",
		},
		{
			name: "empty tag",
			vs:   []VariantDecl{{Name: "blank", BuildTags: []string{" "}}},
			want: "empty build tag",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(context.Background(), ConfigFile(cfgPath), WithVariantDecls(tc.vs...))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Compile error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// 表达式求值对齐 go build 的隐式 tag：GOOS/GOARCH/release tag 与 -tags 一样
// 参与判定，避免「本地命中、CI 不命中」的静默分叉。
func TestEvaluateBuildConstraint(t *testing.T) {
	for _, tc := range []struct {
		expr string
		want bool
	}{
		{"", true},
		{"linux || darwin", true},
		{"linux && !darwin", false},
		{"go1.1", true},
	} {
		got, err := EvaluateBuildConstraint(tc.expr)
		if err != nil {
			t.Fatalf("EvaluateBuildConstraint(%q): %v", tc.expr, err)
		}
		if got != tc.want {
			t.Errorf("EvaluateBuildConstraint(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}
	if _, err := EvaluateBuildConstraint("a &&"); err == nil {
		t.Fatal("invalid expression must return an error")
	}
}

// 命中变体的组件增量成为普通计划节点：键、写法和手写 Component 相同，来源
// 标为 variant:<name>，声明段进节点 Segments（Q8 的段消费视图）。
func TestVariantEffects_ComponentProjection(t *testing.T) {
	setTestBuildTags(t, "telemetry")
	cfgPath := writePlanConfig(t, "variant-component", "")
	p, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithVariantDecls(VariantDecl{
			Name: "telemetry", When: "telemetry",
			Effects: VariantEffect{Components: []VariantComponent{{Name: "telemetry-svc", Config: "biz.telemetry"}}},
		}),
		WithVariantImpl(VariantImpl{
			Name:       "telemetry",
			Components: []Component{planComponent{name: "telemetry-svc"}},
		}),
	)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	n := nodeByKey(p, "component.telemetry-svc")
	if n == nil {
		t.Fatalf("variant component node missing: %+v", p.Nodes)
	}
	if n.Origin != OriginVariant || n.Variant != "telemetry" {
		t.Fatalf("component node must carry variant origin, got %+v", *n)
	}
	if len(n.Segments) != 1 || n.Segments[0] != "biz.telemetry" {
		t.Errorf("declared config segment must reach the node, got %v", n.Segments)
	}
	if !n.Opaque || n.Reload != ReloadReconcile {
		t.Errorf("variant component keeps component node semantics, got %+v", *n)
	}

	var explain bytes.Buffer
	p.Explain(&explain)
	if !strings.Contains(explain.String(), "component.telemetry-svc") ||
		!strings.Contains(explain.String(), "variant:telemetry") {
		t.Errorf("plan must show the variant component with its origin:\n%s", explain.String())
	}
}

// 未命中的变体不产节点，但声明增量仍进计划——运维不命中也能回答「命中会
// 加什么」（Q7 的核心承诺）。
func TestVariantEffects_InactiveDeclarationStaysVisible(t *testing.T) {
	cfgPath := writePlanConfig(t, "variant-inactive", "")
	p, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithVariantDecls(VariantDecl{
			Name: "telemetry", When: "nacos",
			Effects: VariantEffect{Components: []VariantComponent{{Name: "telemetry-svc", Config: "biz.telemetry"}}},
		}),
	)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if n := nodeByKey(p, "component.telemetry-svc"); n != nil {
		t.Fatalf("inactive variant must not contribute nodes: %+v", *n)
	}
	var explain bytes.Buffer
	p.Explain(&explain)
	if !strings.Contains(explain.String(), "component telemetry-svc (segment biz.telemetry)") {
		t.Errorf("inactive declaration must still list its effects:\n%s", explain.String())
	}
	var doctor bytes.Buffer
	writeDoctor(&doctor, p, nil, nil, domainReport{})
	if !strings.Contains(doctor.String(), "component telemetry-svc (segment biz.telemetry)") {
		t.Errorf("doctor must list undeclared-in-binary effects:\n%s", doctor.String())
	}
}

// 契约补丁改写计划与运行时看到的 cfg；覆盖空值不需要理由。
func TestVariantEffects_PatchApplied(t *testing.T) {
	cfgPath := writePlanConfig(t, "variant-patch", "")
	p, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithVariantDecls(VariantDecl{
			Name:    "prod-env",
			Effects: VariantEffect{Patches: []VariantPatch{{Path: "app.env", Value: "prod"}}},
		}),
	)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if p.App.Env != "prod" {
		t.Fatalf("patch must reach the plan's app meta, got %q", p.App.Env)
	}
	for _, w := range p.Warnings {
		if w.Code == "variant-patch" {
			t.Errorf("filling an empty field must not emit a warning, got %+v", w)
		}
	}
}

// I4 同款：覆盖契约里的非空值必须给 Reason；给了就产可见诊断。
func TestVariantEffects_PatchOverrideRequiresReason(t *testing.T) {
	cfgPath := writePlanConfig(t, "variant-patch-override", "  env: \"dev\"\n")
	patch := func(reason string) VariantPatch {
		return VariantPatch{Path: "app.env", Value: "prod", Reason: reason}
	}
	decl := func(reason string) VariantDecl {
		return VariantDecl{Name: "prod-env", Effects: VariantEffect{Patches: []VariantPatch{patch(reason)}}}
	}

	if _, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithVariantDecls(decl("")),
	); err == nil || !strings.Contains(err.Error(), "provide a Reason") {
		t.Fatalf("override without Reason must fail the plan, got %v", err)
	}

	p, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithVariantDecls(decl("production deployment")),
	)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if p.App.Env != "prod" {
		t.Errorf("patched env = %q, want prod", p.App.Env)
	}
	found := false
	for _, w := range p.Warnings {
		if w.Code == "variant-patch" && w.Subject == "app.env" {
			found = true
			if !strings.Contains(w.Message, "production deployment") {
				t.Errorf("warning must carry the reason, got %q", w.Message)
			}
		}
	}
	if !found {
		t.Errorf("override must emit a visible variant-patch warning: %+v", p.Warnings)
	}
}

// 两个生效变体改同一路径为不同值 = 计划自相矛盾，不猜谁赢。
func TestVariantEffects_PatchCrossVariantConflict(t *testing.T) {
	cfgPath := writePlanConfig(t, "variant-patch-conflict", "")
	_, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithVariantDecls(
			VariantDecl{Name: "prod", Effects: VariantEffect{Patches: []VariantPatch{{Path: "app.env", Value: "prod"}}}},
			VariantDecl{Name: "stage", Effects: VariantEffect{Patches: []VariantPatch{{Path: "app.env", Value: "stage"}}}},
		),
	)
	if err == nil || !strings.Contains(err.Error(), "two active variants cannot disagree") {
		t.Fatalf("conflicting patches must fail the plan, got %v", err)
	}
}

// 命中变体的 REST 路由进计划清单，来源指向变体；真实挂载由 WrapHTTP 提供。
func TestVariantEffects_RouteProjection(t *testing.T) {
	setTestBuildTags(t, "grpcgw")
	cfgPath := writePlanConfig(t, "variant-routes", "")
	p, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithHTTP(http.NewServeMux()),
		WithVariantDecls(VariantDecl{
			Name: "grpcgw", When: "grpcgw",
			Effects: VariantEffect{Routes: []VariantRoute{{Method: "get", Path: "/gw/v1/ping", RPC: "Ping"}}},
		}),
		WithVariantImpl(VariantImpl{
			Name:     "grpcgw",
			WrapHTTP: func(next http.Handler) http.Handler { return next },
		}),
	)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	var got *Route
	for i := range p.Routes {
		if p.Routes[i].Path == "/gw/v1/ping" {
			got = &p.Routes[i]
		}
	}
	if got == nil {
		t.Fatalf("variant route missing from plan: %+v", p.Routes)
	}
	if got.Domain != "variant:grpcgw" || got.Variant != "grpcgw" || got.Method != "GET" || got.RPC != "Ping" {
		t.Fatalf("variant route projection = %+v", *got)
	}

	var explain bytes.Buffer
	p.Explain(&explain)
	if !strings.Contains(explain.String(), "variant:grpcgw") || !strings.Contains(explain.String(), "/gw/v1/ping") {
		t.Errorf("plan must show variant routes:\n%s", explain.String())
	}
}

// mergeRoutes 对变体路由不设特权通道：与域路由同一冲突判据（完全重复 /
// gin 通配形态），并在合并后保持排序。
func TestVariantEffects_MergeRouteConflicts(t *testing.T) {
	base := []Route{{Domain: "greet", Method: "GET", Path: "/greet/:id"}}

	if _, err := mergeRoutes(base, []Route{{Domain: "variant:x", Method: "GET", Path: "/greet/:id", Variant: "x"}}); err == nil ||
		!strings.Contains(err.Error(), "is also declared by") {
		t.Fatalf("exact duplicate must fail, got %v", err)
	}
	if _, err := mergeRoutes(base, []Route{{Domain: "variant:x", Method: "GET", Path: "/greet/:name", Variant: "x"}}); err == nil ||
		!strings.Contains(err.Error(), "wildcards") {
		t.Fatalf("gin wildcard conflict must fail, got %v", err)
	}
	merged, err := mergeRoutes(base, []Route{{Domain: "variant:x", Method: "GET", Path: "/gw/ping", Variant: "x"}})
	if err != nil {
		t.Fatalf("mergeRoutes: %v", err)
	}
	if len(merged) != 2 || merged[0].Path != "/greet/:id" || merged[1].Path != "/gw/ping" {
		t.Fatalf("merged routes must be sorted, got %+v", merged)
	}
}

// 两个生效变体声明同一路由：不是「谁赢」，是配置错误。
func TestVariantEffects_DuplicateRouteAcrossVariants(t *testing.T) {
	setTestBuildTags(t, "a", "b")
	cfgPath := writePlanConfig(t, "variant-route-dupe", "")
	wrap := func(next http.Handler) http.Handler { return next }
	_, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithHTTP(http.NewServeMux()),
		WithVariantDecls(
			VariantDecl{Name: "a", When: "a", Effects: VariantEffect{Routes: []VariantRoute{{Method: "GET", Path: "/same"}}}},
			VariantDecl{Name: "b", When: "b", Effects: VariantEffect{Routes: []VariantRoute{{Method: "GET", Path: "/same"}}}},
		),
		WithVariantImpl(VariantImpl{Name: "a", WrapHTTP: wrap}),
		WithVariantImpl(VariantImpl{Name: "b", WrapHTTP: wrap}),
	)
	if err == nil || !strings.Contains(err.Error(), "is also declared by") {
		t.Fatalf("duplicate variant route must fail the plan, got %v", err)
	}
}

// I12 对账：声明与 //go:build 实现不一致（多/少/条件漂移）都在计划期报错，
// 不让装配与声明各说各话。
func TestVariantEffects_RejectsImplDrift(t *testing.T) {
	cfgPath := writePlanConfig(t, "variant-drift", "")
	wrap := func(next http.Handler) http.Handler { return next }
	comp := func(name string) Component { return planComponent{name: name} }

	for _, tc := range []struct {
		name  string
		decls []VariantDecl
		impls []VariantImpl
		opts  []Option
		want  string
	}{
		{
			name:  "impl without declaration",
			impls: []VariantImpl{{Name: "ghost"}},
			want:  "is not declared",
		},
		{
			name:  "impl for inactive variant",
			decls: []VariantDecl{{Name: "gw", When: "nacos"}},
			impls: []VariantImpl{{Name: "gw"}},
			want:  "has drifted from the declaration",
		},
		{
			name:  "empty impl name",
			impls: []VariantImpl{{}},
			want:  "implementation requires a Name",
		},
		{
			name:  "impl registered twice",
			decls: []VariantDecl{{Name: "gw"}},
			impls: []VariantImpl{{Name: "gw"}, {Name: "gw"}},
			want:  "registered twice",
		},
		{
			name: "declared component without impl",
			decls: []VariantDecl{{Name: "gw", Effects: VariantEffect{
				Components: []VariantComponent{{Name: "a"}},
			}}},
			want: "no implementation is compiled in",
		},
		{
			name: "impl misses declared component",
			decls: []VariantDecl{{Name: "gw", Effects: VariantEffect{
				Components: []VariantComponent{{Name: "a"}},
			}}},
			impls: []VariantImpl{{Name: "gw", Components: []Component{comp("b")}}},
			want:  `does not provide it`,
		},
		{
			name: "impl provides undeclared component",
			decls: []VariantDecl{{Name: "gw", Effects: VariantEffect{
				Components: []VariantComponent{{Name: "a"}},
			}}},
			impls: []VariantImpl{{Name: "gw", Components: []Component{comp("a"), comp("b")}}},
			want:  "undeclared component(s) b",
		},
		{
			name:  "impl components without declaration",
			decls: []VariantDecl{{Name: "gw"}},
			impls: []VariantImpl{{Name: "gw", Components: []Component{comp("a")}}},
			want:  "declaration lists none",
		},
		{
			name:  "wrap without routes",
			decls: []VariantDecl{{Name: "gw"}},
			impls: []VariantImpl{{Name: "gw", WrapHTTP: wrap}},
			want:  "wraps HTTP but the declaration lists no routes",
		},
		{
			name: "routes without wrap",
			decls: []VariantDecl{{Name: "gw", Effects: VariantEffect{
				Routes: []VariantRoute{{Method: "GET", Path: "/gw/ping"}},
			}}},
			impls: []VariantImpl{{Name: "gw"}},
			opts:  []Option{WithHTTP(http.NewServeMux())},
			want:  "provides no WrapHTTP",
		},
		{
			name: "routes without HTTP face",
			decls: []VariantDecl{{Name: "gw", Effects: VariantEffect{
				Routes: []VariantRoute{{Method: "GET", Path: "/gw/ping"}},
			}}},
			impls: []VariantImpl{{Name: "gw", WrapHTTP: wrap}},
			want:  "has no HTTP face",
		},
		{
			name: "component declared by two variants",
			decls: []VariantDecl{
				{Name: "a", Effects: VariantEffect{Components: []VariantComponent{{Name: "shared"}}}},
				{Name: "b", Effects: VariantEffect{Components: []VariantComponent{{Name: "shared"}}}},
			},
			want: "declared by variant",
		},
		{
			name: "unsupported route method",
			decls: []VariantDecl{{Name: "gw", Effects: VariantEffect{
				Routes: []VariantRoute{{Method: "FETCH", Path: "/gw/ping"}},
			}}},
			want: "not a supported HTTP method",
		},
		{
			name: "patch on missing field",
			decls: []VariantDecl{{Name: "gw", Effects: VariantEffect{
				Patches: []VariantPatch{{Path: "nope.field", Value: "x"}},
			}}},
			want: "does not exist",
		},
		{
			name: "patch on non-string field",
			decls: []VariantDecl{{Name: "gw", Effects: VariantEffect{
				Patches: []VariantPatch{{Path: "server.http", Value: "x"}},
			}}},
			want: "is not a string field",
		},
		{
			name: "empty component name",
			decls: []VariantDecl{{Name: "gw", Effects: VariantEffect{
				Components: []VariantComponent{{Name: " "}},
			}}},
			want: "empty name",
		},
		{
			name: "empty patch path",
			decls: []VariantDecl{{Name: "gw", Effects: VariantEffect{
				Patches: []VariantPatch{{Path: " ", Value: "x"}},
			}}},
			want: "empty path",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := []Option{ConfigFile(cfgPath)}
			opts = append(opts, tc.opts...)
			opts = append(opts, WithVariantDecls(tc.decls...))
			for _, impl := range tc.impls {
				opts = append(opts, WithVariantImpl(impl))
			}
			_, err := Compile(context.Background(), opts...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Compile error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// 运行期配置变更会重算同一份 Decl：组件与 HTTP 包装只能装一次，补丁每次
// 重放（契约被重载后仍须生效）。
func TestVariantEffects_ReapplyIsIdempotent(t *testing.T) {
	setTestBuildTags(t, "gw")
	var wraps int32
	d := newDecl(
		WithHTTP(http.NewServeMux()),
		WithVariantDecls(VariantDecl{
			Name: "gw", When: "gw",
			Effects: VariantEffect{
				Components: []VariantComponent{{Name: "gw-svc"}},
				Routes:     []VariantRoute{{Method: "GET", Path: "/gw/ping"}},
				Patches:    []VariantPatch{{Path: "app.env", Value: "prod"}},
			},
		}),
		WithVariantImpl(VariantImpl{
			Name:       "gw",
			Components: []Component{planComponent{name: "gw-svc"}},
			WrapHTTP: func(next http.Handler) http.Handler {
				atomic.AddInt32(&wraps, 1)
				return next
			},
		}),
	)
	cfg := bconf.NewBootstrap()
	for i := 0; i < 2; i++ {
		if _, err := applyVariantEffects(d, cfg); err != nil {
			t.Fatalf("applyVariantEffects (pass %d): %v", i+1, err)
		}
	}
	if n := atomic.LoadInt32(&wraps); n != 1 {
		t.Errorf("HTTP wrapper applied %d time(s), want 1", n)
	}
	if len(d.Components) != 1 {
		t.Errorf("components applied %d time(s), want 1", len(d.Components))
	}
	if got := cfg.GetApp().GetEnv(); got != "prod" {
		t.Errorf("patch must survive reapply, env = %q", got)
	}
}

// 变体声明顺序不影响计划（I2）：Effect 的数据面与手写声明同一哈希口径。
func TestVariantEffects_PermutationInvariant(t *testing.T) {
	setTestBuildTags(t, "a", "b")
	cfgPath := writePlanConfig(t, "variant-perm", "")
	declA := VariantDecl{Name: "a", When: "a", Effects: VariantEffect{
		Components: []VariantComponent{{Name: "comp-a"}},
		Patches:    []VariantPatch{{Path: "app.env", Value: "a-env"}},
	}}
	declB := VariantDecl{Name: "b", When: "b", Effects: VariantEffect{
		Components: []VariantComponent{{Name: "comp-b"}},
	}}
	implA := WithVariantImpl(VariantImpl{Name: "a", Components: []Component{planComponent{name: "comp-a"}}})
	implB := WithVariantImpl(VariantImpl{Name: "b", Components: []Component{planComponent{name: "comp-b"}}})

	compile := func(order ...VariantDecl) string {
		p, err := Compile(context.Background(), ConfigFile(cfgPath),
			WithVariantDecls(order...), implA, implB)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		return p.Hash()
	}
	if h1, h2 := compile(declA, declB), compile(declB, declA); h1 != h2 {
		t.Fatalf("variant declaration order changed the plan hash: %s != %s", h1, h2)
	}
}
