// variant_test.go 钉住 Q7 的计划投影口径：变体声明恒进 Plan.Variants（未命中
// 也列出，供 --plan/doctor 审计声明面），命中由 appkit 按二进制实际的构建
// 约束求值（go/build/constraint）并另产一条 ambient 诊断；声明面本身的两类
// 错误（缺 Name、重复 Name）与条件形式错误必须在计划期拒绝。
package appkit

import (
	"bytes"
	"context"
	"strings"
	"testing"
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

	// 命中的变体才产 ambient 诊断；未命中的声明不制造 warn 噪声。
	var subjects []string
	for _, w := range p.Warnings {
		if w.Code == "ambient" && strings.HasPrefix(w.Subject, "variant:") {
			subjects = append(subjects, w.Subject)
		}
	}
	if len(subjects) != 1 || subjects[0] != "variant:grpcgw" {
		t.Errorf("ambient diagnostics must cover exactly the active variant, got %v", subjects)
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

// 未命中不产诊断；无条件声明（When 空，仅手写可达）恒命中并可见。
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
	variants := 0
	for _, w := range p.Warnings {
		if w.Code == "ambient" && w.Subject == "variant:always" {
			variants++
		}
	}
	if variants != 1 {
		t.Fatalf("unconditional variant must surface one ambient diagnostic, got %d: %+v", variants, p.Warnings)
	}
}

// 迁移期两套声明并存（手写 WithAmbient + AppSpec.variants）：命中变体与
// WithAmbient 共用同一身份空间（Subject = "variant:<name>"）——同说明合并为
// 一条诊断，异说明在计划期报错，不能让同一逃逸舱出现两种描述。
func TestVariantProjection_SharesAmbientIdentity(t *testing.T) {
	setTestBuildTags(t, "grpcgw")
	cfgPath := writePlanConfig(t, "variant-ambient", "")
	compile := func(detail string) (*Plan, error) {
		return Compile(context.Background(), ConfigFile(cfgPath),
			WithVariantDecls(VariantDecl{Name: "grpcgw", When: "grpcgw", Detail: detail}),
			WithAmbient(Ambient{Name: "variant:grpcgw", Detail: detail}),
		)
	}

	p, err := compile("gateway wiring behind grpcgw")
	if err != nil {
		t.Fatalf("Compile (same detail): %v", err)
	}
	count := 0
	for _, w := range p.Warnings {
		if w.Code == "ambient" && w.Subject == "variant:grpcgw" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("same identity + same detail must merge into one diagnostic, got %d: %+v", count, p.Warnings)
	}

	if _, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithVariantDecls(VariantDecl{Name: "grpcgw", When: "grpcgw", Detail: "from the spec"}),
		WithAmbient(Ambient{Name: "variant:grpcgw", Detail: "from init()"}),
	); err == nil || !strings.Contains(err.Error(), `ambient "variant:grpcgw" declared twice`) {
		t.Fatalf("conflicting detail for one identity must fail the plan, got %v", err)
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
