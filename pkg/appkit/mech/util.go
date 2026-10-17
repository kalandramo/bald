// util.go 是协调配套的纯工具函数（不依赖 App 任何状态）。
//
// B5 落点说明（装配层评估.md §9.3）：DiffStrings 实现体自批 2 起移入本包，
// appkit 侧旧名删除——appkit 内部无生产引用，生成物（internal/codegen 模板）
// 同批改经 mech.DiffStrings 调用。ParseStringList 实现体仍留 appkit（其唯一
// appkit 内部消费者 ReconcileCtx.StringSlice 是接收者方法，实现体入 mech 会
// 造出双份实现；目标归宿 bootstrap/config，随批 3 职责边界落点迁移），本包
// 保留 mech 名下的薄转发——两个名字、一份实现。
package mech

import (
	"sort"

	"github.com/kalandramo/bald/pkg/appkit"
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
// 与逗号分隔的字符串；空值返回 nil。语义与 appkit.ParseStringList 一致
// （实现体在 appkit，见文件头落点说明）。
func ParseStringList(v any) []string {
	return appkit.ParseStringList(v)
}
