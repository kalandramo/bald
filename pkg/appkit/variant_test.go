// variant_test.go 钉住 Q7 的计划投影口径：变体声明恒进 Plan.Variants（未命中
// 也列出，供 --plan/doctor 审计声明面），命中的变体另产一条 ambient 诊断；
// 声明面本身的两类错误（缺 Name、重复 Name）必须在计划期拒绝。
package appkit

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVariantProjection_DeclarationsAlwaysInPlan(t *testing.T) {
	cfgPath := writePlanConfig(t, "variant-app", "")
	p, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithVariantDecls(
			VariantDecl{Name: "nacos_config", BuildTags: []string{"nacos", "config"}},
			VariantDecl{Name: "grpcgw", BuildTags: []string{"grpcgw"}, Detail: "gateway wiring", Active: true},
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
	if !p.Variants[0].Active || p.Variants[1].Active {
		t.Errorf("active flags must be preserved per declaration: %+v", p.Variants)
	}
	if p.Variants[0].Detail != "gateway wiring" {
		t.Errorf("declared detail must be kept, got %q", p.Variants[0].Detail)
	}
	if p.Variants[1].Detail == "" {
		t.Error("a variant without description must get a default detail (diagnostics must read standalone)")
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
		"tags=nacos,config",
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
		"tags=nacos,config",
	} {
		if !strings.Contains(doctor.String(), want) {
			t.Errorf("doctor output missing %q:\n%s", want, doctor.String())
		}
	}
}

// 迁移期两套声明并存（手写 WithAmbient + AppSpec.variants）：命中变体与
// WithAmbient 共用同一身份空间（Subject = "variant:<name>"）——同说明合并为
// 一条诊断，异说明在计划期报错，不能让同一逃逸舱出现两种描述。
func TestVariantProjection_SharesAmbientIdentity(t *testing.T) {
	cfgPath := writePlanConfig(t, "variant-ambient", "")
	compile := func(detail string) (*Plan, error) {
		return Compile(context.Background(), ConfigFile(cfgPath),
			WithVariantDecls(VariantDecl{Name: "grpcgw", Detail: detail, Active: true}),
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
		WithVariantDecls(VariantDecl{Name: "grpcgw", Detail: "from the spec", Active: true}),
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
			vs:   []VariantDecl{{BuildTags: []string{"grpcgw"}}},
			want: "requires a Name",
		},
		{
			name: "duplicate name",
			vs:   []VariantDecl{{Name: "grpcgw"}, {Name: "grpcgw"}},
			want: "declared twice",
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
