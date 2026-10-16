// util.go 是协调配套的纯工具函数（不依赖 AppKit 任何状态）。
//
// 落点说明：函数体保留在 appkit（单向约束 appkit 不 import mech 使 appkit
// 侧别名无法委托 mech；ReconcileCtx.StringSlice 与生成物 internal/codegen
// 经 appkit 名调用），本包提供 mech 名下的薄转发别名——两个名字、一份实现。
package mech

import "github.com/kalandramo/bald/pkg/appkit"

// DiffStrings 返回期望集与实际集的差集（新增 / 移除，均升序稳定），便于业务
// 写「期望有实际无→挂载、实际有期望无→卸载」的协调逻辑。语义与
// appkit.DiffStrings 一致。
func DiffStrings(want, have []string) (add, remove []string) {
	return appkit.DiffStrings(want, have)
}

// ParseStringList 把配置值解析为字符串列表：支持 []string、[]any（yaml 列表）
// 与逗号分隔的字符串；空值返回 nil。语义与 appkit.ParseStringList 一致。
func ParseStringList(v any) []string {
	return appkit.ParseStringList(v)
}
