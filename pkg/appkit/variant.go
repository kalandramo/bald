package appkit

import (
	"fmt"
	"sort"
)

// VariantDecl 是 AppSpec.variants 的运行期投影（Q7）：无论是否命中都进计划
// ——不带 tag 的构建也能回答「这个应用声明了哪些变体」；命中的变体另产一条
// ambient 诊断（Subject = "variant:<name>"）。Active 由生成物按当前二进制
// 实际带有的 build tag 计算，应用侧不猜 tag 组合。
type VariantDecl struct {
	Name      string   `json:"name"`
	BuildTags []string `json:"buildTags,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	Active    bool     `json:"active"`
}

// WithVariantDecls 声明构建期变体（可多次调用，追加；计划期按 Name 去重）。
func WithVariantDecls(vs ...VariantDecl) Option {
	return optionFunc(func(d *Decl) { d.Variants = append(d.Variants, vs...) })
}

// variantProjection 把变体声明规范化为计划字段与命中诊断：Name 必填、
// 重复 Name 报错（一个身份不能描述两个变体）；命中的变体追加 ambient
// 诊断，最终顺序由 Plan 收尾统一排序（置换不变）。
func variantProjection(vs []VariantDecl) ([]VariantDecl, []Diagnostic, error) {
	if len(vs) == 0 {
		return nil, nil, nil
	}
	seen := make(map[string]bool, len(vs))
	out := make([]VariantDecl, 0, len(vs))
	var warns []Diagnostic
	for _, v := range vs {
		if v.Name == "" {
			return nil, nil, fmt.Errorf("appkit: variant declaration requires a Name")
		}
		if seen[v.Name] {
			return nil, nil, fmt.Errorf("appkit: variant %q declared twice", v.Name)
		}
		seen[v.Name] = true
		detail := v.Detail
		if detail == "" {
			detail = "wiring injected by the build-tag variant is not described by the plan"
		}
		out = append(out, VariantDecl{
			Name:      v.Name,
			BuildTags: append([]string(nil), v.BuildTags...),
			Detail:    detail,
			Active:    v.Active,
		})
		if v.Active {
			warns = append(warns, Diagnostic{
				Level:   DiagWarn,
				Code:    "ambient",
				Subject: "variant:" + v.Name,
				Message: detail,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, warns, nil
}
