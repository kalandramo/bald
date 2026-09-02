// harness.go 提供协调器逻辑的测试装置（harness）。
//
// 为什么需要导出的装置 API：examples/*、tests/* 是独立 Go module，无法使用
// 包内 export_test.go 暴露未导出成员的惯用法；它们的集成测试需要一个处于
// 运行态、可不启动 server 的 AppKit 来驱动 ReconcileCtx.Mount/Unmount。
// 生产代码不应使用本装置——真实生命周期由 New + Run 建立。
package appkit

// NewHarness 构造一个测试装置：跳过配置加载与服务器启动，直接进入运行态
// （MountComponent/ReconcileCtx.Mount 可用），供跨 module 的集成测试驱动
// 协调器逻辑（配合 NewReconcileCtx 显式注入 Viper 后手动调用协调函数）。
//
// 装置不监听端口、不加载配置（Viper 为 nil）；组件挂载/卸载与停机 Dispose
// 语义与真实 AppKit 一致。
func NewHarness() *AppKit {
	a := New()
	a.running.Store(true)
	return a
}
