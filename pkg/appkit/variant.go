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

// variantProjection 把变体声明规范化为计划字段：Name 必填、重复 Name 报错
// （一个身份不能描述两个变体），说明为空时补默认 Detail；最终顺序由 Plan
// 收尾统一排序（置换不变）。命中诊断不在这里生成——见 variantAmbients。
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
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// variantAmbients 返回命中变体的 ambient 声明，交给 ambientWarnings 与手写
// WithAmbient 一起裁决：同一 Subject（variant:<name>）同说明合并、异说明
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
