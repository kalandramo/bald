package appkit

import (
	"fmt"
	"sort"
	"strings"
)

// VariantDecl 是 AppSpec.variants 的运行期投影（Q7）：无论是否命中都进计划
// ——不带 tag 的构建也能回答「这个应用声明了哪些变体」；命中的变体另产一条
// ambient 诊断（Subject = "variant:<name>"）。Active 由 Compile 按当前二进制
// 实际的构建约束求值算出，应用侧不猜 tag 组合。
type VariantDecl struct {
	Name string `json:"name"`
	// When 是 Go 构建约束表达式（`//go:build` 行语法，如
	// "grpcgw && !nacos"）；空 = 无条件（手写声明的「总是命中」形态，
	// 生成物必须声明条件——Gate1 拒绝空条件）。
	When string `json:"when,omitempty"`
	// BuildTags 是 When 的迁移糖：各项均须命中，等价于用 && 连接。
	// 与 When 互斥（同时声明在计划期报错），新声明请直接写 When。
	BuildTags []string `json:"buildTags,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	// Active 是命中结果（Compile 计算并写回；输入值被忽略）。
	Active bool `json:"active"`
}

// WithVariantDecls 声明构建期变体（可多次调用，追加；计划期按 Name 去重）。
func WithVariantDecls(vs ...VariantDecl) Option {
	return optionFunc(func(d *Decl) { d.Variants = append(d.Variants, vs...) })
}

// variantProjection 把变体声明规范化为计划字段：Name 必填、重复 Name 报错
// （一个身份不能描述两个变体），When 与 BuildTags 合并为唯一表达式、按当前
// 二进制的构建约束求值出 Active；说明为空时补默认 Detail（说明必须能独立
// 读懂）。最终顺序由 Plan 收尾统一排序（置换不变）。命中诊断不在这里生成
// ——见 variantAmbients。
func variantProjection(vs []VariantDecl) ([]VariantDecl, error) {
	if len(vs) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(vs))
	out := make([]VariantDecl, 0, len(vs))
	for _, v := range vs {
		if v.Name == "" {
			return nil, fmt.Errorf("appkit: variant declaration requires a Name")
		}
		if seen[v.Name] {
			return nil, fmt.Errorf("appkit: variant %q declared twice", v.Name)
		}
		seen[v.Name] = true
		when, err := canonicalWhen(v)
		if err != nil {
			return nil, err
		}
		active, err := EvaluateBuildConstraint(when)
		if err != nil {
			return nil, fmt.Errorf("appkit: variant %q: %w", v.Name, err)
		}
		detail := v.Detail
		if detail == "" {
			if when == "" {
				detail = "variant declared in AppSpec.variants (unconditional)"
			} else {
				detail = "build-tag variant declared in AppSpec.variants (when: " + when + ")"
			}
		}
		out = append(out, VariantDecl{
			Name:      v.Name,
			When:      when,
			BuildTags: append([]string(nil), v.BuildTags...),
			Detail:    detail,
			Active:    active,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// canonicalWhen 把 When/BuildTags 两种声明形式归一为唯一表达式：两者互斥，
// BuildTags 以 && 连接；结果必须是 go/build/constraint 能解析的表达式。
// 空表达式 = 无条件（仅手写声明可达；生成物由 Gate1 要求条件非空）。
func canonicalWhen(v VariantDecl) (string, error) {
	when := strings.TrimSpace(v.When)
	if when != "" && len(v.BuildTags) > 0 {
		return "", fmt.Errorf("appkit: variant %q declares both When and BuildTags (use When; BuildTags is join-by-&& sugar)", v.Name)
	}
	if when == "" && len(v.BuildTags) > 0 {
		tags := make([]string, 0, len(v.BuildTags))
		for _, t := range v.BuildTags {
			if t = strings.TrimSpace(t); t == "" {
				return "", fmt.Errorf("appkit: variant %q has an empty build tag", v.Name)
			}
			tags = append(tags, t)
		}
		when = strings.Join(tags, " && ")
	}
	if when == "" {
		return "", nil
	}
	if _, err := EvaluateBuildConstraint(when); err != nil {
		return "", fmt.Errorf("appkit: variant %q: %w", v.Name, err)
	}
	return when, nil
}

// variantAmbients 返回命中变体的 ambient 声明，交给 ambientWarnings 与手写// WithAmbient 一起裁决：同一 Subject（variant:<name>）同说明合并、异说明
// 报错——声明的身份空间只有一套，不能因为来源不同就出现两种描述。
func variantAmbients(vs []VariantDecl) []Ambient {
	var out []Ambient
	for _, v := range vs {
		if v.Active {
			out = append(out, Ambient{Name: "variant:" + v.Name, Detail: v.Detail})
		}
	}
	return out
}

// variantWhenLabel 是变体条件的展示形态：无条件声明显式写 (always)，
// 不打印空串（doctor/--plan 的输出必须自解释）。
func variantWhenLabel(v VariantDecl) string {
	if v.When == "" {
		return "(always)"
	}
	return v.When
}
