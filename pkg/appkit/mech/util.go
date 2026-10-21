// util.go 是协调配套的纯工具函数（不依赖 App 任何状态）。
//
// DiffStrings 供协调逻辑做集合差；生成物（internal/codegen 模板）也经
// mech.DiffStrings 调用。
//
// ParseStringList 薄委托 bootstrap/config.StringList（配置值解析属装载层）——
// 用户新代码请直接用 config.StringList；本名保留为兼容别名。
package mech

import (
	"sort"

	"github.com/kalandramo/bald/bootstrap/config"
)

// DiffStrings 返回期望集与实际集的差集（新增 / 移除，均升序稳定），便于业务
// 写「期望有实际无→挂载、实际有期望无→卸载」的协调逻辑。
func DiffStrings(want, have []string) (add, remove []string) {
	wantSet := make(map[string]struct{}, len(want))
	haveSet := make(map[string]struct{}, len(have))
	for _, w := range want {
		wantSet[w] = struct{}{}
	}
	for _, h := range have {
		haveSet[h] = struct{}{}
	}
	for w := range wantSet {
		if _, ok := haveSet[w]; !ok {
			add = append(add, w)
		}
	}
	for h := range haveSet {
		if _, ok := wantSet[h]; !ok {
			remove = append(remove, h)
		}
	}
	sort.Strings(add)
	sort.Strings(remove) // 稳定顺序：协调结果可预期、便于测试
	return add, remove
}

// ParseStringList 把配置值解析为字符串列表：支持 []string、[]any（yaml 列表）
// 与逗号分隔的字符串；空值返回 nil。薄委托 config.StringList——用户新代码
// 请直接用 config.StringList。
func ParseStringList(v any) []string {
	return config.StringList(v)
}
