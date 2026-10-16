// Package mech 承载 appkit 的高级机制面：T1 效应账本、S1 能力声明、C1 组件
// 生命周期、R1 key 级配置订阅与期望态协调
// （docs/devel/zh-CN/装配层评估.md §4.4/§6、D1-target-appkit.md §5）。
//
// 分层契约：
//   - mech 单向 import appkit；appkit 不 import mech。
//   - 机制构造器经 AppKit 的导出注册方法（AddEffect/AddProvides/...，见
//     pkg/appkit/register.go）写入状态，返回 appkit.Option（本批仍是
//     func(*appkit.AppKit)，接口化为批 2 的破坏性变更）。
//   - 引擎消费的类型（Component、ReconcileCtx、ReconcileFunc 等）仍归
//     appkit，本包经 appkit.X 引用。
//   - appkit 包内保留同名构造函数为一行薄转发别名，旧调用点与现有测试
//     无需迁移；本批不删除任何旧名。
package mech

import (
	"context"
	"time"

	"github.com/kalandramo/bald/pkg/appkit"
)

// Effect 登记一条可逆效应：undo 在停机（stopAll 阶段 0，先于 BeforeStop 钩子）
// 或启动失败回滚时逆序执行。契约（每条 undo 独立超时、panic 隔离、回放幂等）
// 与 appkit.Effect 完全一致，详见其文档。
func Effect(name string, undo func(ctx context.Context) error) appkit.Option {
	return func(a *appkit.AppKit) { a.AddEffect(name, undo) }
}

// EffectTimeout 设置效应撤销阶段的独立超时（每条 undo 各享此超时）。
func EffectTimeout(d time.Duration) appkit.Option {
	return func(a *appkit.AppKit) { a.SetEffectTimeout(d) }
}

// Provides 声明本进程将提供的能力（可多次调用，追加）。只声明确定拥有的能力；
// 同一能力重复声明视为装配笔误，Resolve 启动期报错。
func Provides(caps ...string) appkit.Option {
	return func(a *appkit.AppKit) { a.AddProvides(caps...) }
}

// Requires 声明组件 component 依赖的能力。缺失能力在 Resolve 启动期聚合报错
// （fail-fast）；同一 (component, cap) 重复声明幂等。
func Requires(component string, caps ...string) appkit.Option {
	return func(a *appkit.AppKit) { a.AddRequires(component, caps...) }
}

// Components 注册进程内组件（可多次调用，追加）：顺序 Start（注册序=依赖
// 建立序）、逆序 Dispose（停机对称）、panic 隔离、独立超时。
func Components(comps ...appkit.Component) appkit.Option {
	return func(a *appkit.AppKit) { a.AddComponents(comps...) }
}

// ComponentTimeout 设置组件 Start/Dispose 的独立超时。
func ComponentTimeout(d time.Duration) appkit.Option {
	return func(a *appkit.AppKit) { a.SetComponentTimeout(d) }
}

// OnKeyChange 订阅单个配置 key 的变更：仅当该 key 新值与上次观测值不同才触发
// fn(old, new)；可与 OnConfigChange 叠加使用。key 为点路径（"http.addr"）。
func OnKeyChange(key string, fn func(old, new string)) appkit.Option {
	return func(a *appkit.AppKit) { a.AddKeyWatch(key, fn) }
}

// Reconcile 注册一个期望态协调器（R1-2）：框架以配置为期望态、diff 实际态后
// 收敛；单个协调器失败仅记日志，由下次协调补齐，不阻断整机。
func Reconcile(name string, fn appkit.ReconcileFunc) appkit.Option {
	return func(a *appkit.AppKit) { a.AddReconcile(name, fn) }
}
