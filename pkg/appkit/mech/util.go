// util.go 是协调配套的纯工具函数（不依赖 App 任何状态）。
//
// DiffStrings 供协调逻辑做集合差；生成物（internal/codegen 模板）也经
// mech.DiffStrings 调用。
package mech

import (
	"sort"
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
