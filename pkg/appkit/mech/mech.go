// Package mech 承载 appkit 的高级机制面：T1 效应账本、S1 能力声明、C1 组件
// 生命周期、R1 key 级配置订阅与期望态协调
// （docs/devel/zh-CN/装配层评估.md §4.4/§6、D1-target-appkit.md §5）。
//
// 分层契约：
//   - mech 单向 import appkit；appkit 不 import mech。
//   - appkit.Option 自批 2 起为接口 interface{ Apply(*appkit.Decl) }（导出
//     方法 + 导出参数类型，包外可实现）：本包每个构造器返回**自持 option
//     类型**，Apply 直写 Decl 的导出字段——与 appkit 包内构造器（optionFunc
//     适配）共用同一份声明
//     载体，不再经 App 的注册方法中转（register.go 已随 Decl 导出移除）。
//   - 引擎消费的类型（Component、ReconcileCtx、ReconcileFunc 等）仍归
//     appkit，本包经 appkit.X 引用；运行期登记（addEffect 等持锁路径）
//     不经 Decl，保持原样。ComponentFunc 适配器自 B5 起归本包（仅实现
//     appkit.Component 导出接口、无引擎状态依赖）；Registry[T] 见 registry.go。
//   - 机制引擎本身（UndoEffects/Resolve/startComponents/dispatchKeyWatch/
//     runReconcilers）留在 appkit，行为与本包构造器无关。
package mech

import (
	"context"
	"time"

	"github.com/kalandramo/bald/pkg/appkit"
)

// effectOption 是 Effect 的自持 option 类型：Apply 直写 Decl.Effects
// （追加语义，与原写入路径逐字段一致）。
type effectOption struct {
	name string
	undo func(ctx context.Context) error
}

func (o effectOption) Apply(d *appkit.Decl) {
	d.Effects = append(d.Effects, appkit.EffectDecl{Name: o.name, Undo: o.undo})
}

// effectTimeoutOption 是 EffectTimeout 的自持 option 类型（单槽覆盖）。
type effectTimeoutOption time.Duration

func (o effectTimeoutOption) Apply(d *appkit.Decl) { d.EffectTimeout = time.Duration(o) }

// providesOption 是 Provides 的自持 option 类型（追加语义）。
type providesOption []string

func (o providesOption) Apply(d *appkit.Decl) { d.Provides = append(d.Provides, o...) }

// requiresOption 是 Requires 的自持 option 类型（追加语义）。
type requiresOption struct {
	component string
	caps      []string
}

func (o requiresOption) Apply(d *appkit.Decl) {
	d.Requires = append(d.Requires, appkit.RequireDecl{Component: o.component, Caps: o.caps})
}

// componentsOption 是 Components 的自持 option 类型（追加语义）。
type componentsOption []appkit.Component

func (o componentsOption) Apply(d *appkit.Decl) { d.Components = append(d.Components, o...) }

// componentTimeoutOption 是 ComponentTimeout 的自持 option 类型（单槽覆盖）。
type componentTimeoutOption time.Duration

func (o componentTimeoutOption) Apply(d *appkit.Decl) { d.ComponentTimeout = time.Duration(o) }

// keyWatchOption 是 OnKeyChange 的自持 option 类型（追加语义）。
type keyWatchOption struct {
	key string
	fn  func(old, new string)
}

func (o keyWatchOption) Apply(d *appkit.Decl) {
	d.KeyWatches = append(d.KeyWatches, appkit.KeyWatchDecl{Key: o.key, Fn: o.fn})
}

// reconcileOption 是 Reconcile 的自持 option 类型（追加语义）。
type reconcileOption struct {
	name string
	fn   appkit.ReconcileFunc
}

func (o reconcileOption) Apply(d *appkit.Decl) {
	d.Reconciles = append(d.Reconciles, appkit.ReconcileDecl{Name: o.name, Fn: o.fn})
}

// Effect 登记一条可逆效应：undo 在停机（stopAll 阶段 0，先于 BeforeStop 钩子）
// 或启动失败回滚时逆序执行。契约（每条 undo 独立超时、panic 隔离、回放幂等）
// 与 appkit.Effect 完全一致，详见其文档。
func Effect(name string, undo func(ctx context.Context) error) appkit.Option {
	return effectOption{name: name, undo: undo}
}

// EffectTimeout 设置效应撤销阶段的独立超时（每条 undo 各享此超时）。
func EffectTimeout(d time.Duration) appkit.Option {
	return effectTimeoutOption(d)
}

// Provides 声明本进程将提供的能力（可多次调用，追加）。只声明确定拥有的能力；
// 同一能力重复声明视为装配笔误，Resolve 启动期报错。
func Provides(caps ...string) appkit.Option {
	return providesOption(caps)
}

// Requires 声明组件 component 依赖的能力。缺失能力在 Resolve 启动期聚合报错
// （fail-fast）；同一 (component, cap) 重复声明幂等。
func Requires(component string, caps ...string) appkit.Option {
	return requiresOption{component: component, caps: caps}
}

// Components 注册进程内组件（可多次调用，追加）：顺序 Start（注册序=依赖
// 建立序）、逆序 Dispose（停机对称）、panic 隔离、独立超时。
func Components(comps ...appkit.Component) appkit.Option {
	return componentsOption(comps)
}

// ComponentFunc 把一个无启动逻辑的清理函数（如 otel TracerProvider.Shutdown、
// database/sql Close）适配为 appkit.Component（B5 自 appkit 下沉：仅实现
// 导出接口，无引擎状态依赖，类型与适配器同迁）。
func ComponentFunc(name string, dispose func(context.Context) error) appkit.Component {
	return componentFunc{name: name, dispose: dispose}
}

type componentFunc struct {
	name    string
	dispose func(context.Context) error
}

func (f componentFunc) Name() string                { return f.name }
func (f componentFunc) Start(context.Context) error { return nil }
func (f componentFunc) Dispose(ctx context.Context) error {
	if f.dispose == nil {
		return nil
	}
	return f.dispose(ctx)
}

// ComponentTimeout 设置组件 Start/Dispose 的独立超时。
func ComponentTimeout(d time.Duration) appkit.Option {
	return componentTimeoutOption(d)
}

// OnKeyChange 订阅单个配置 key 的变更：仅当该 key 新值与上次观测值不同才触发
// fn(old, new)；可与 OnConfigChange 叠加使用。key 为点路径（"http.addr"）。
func OnKeyChange(key string, fn func(old, new string)) appkit.Option {
	return keyWatchOption{key: key, fn: fn}
}

// Reconcile 注册一个期望态协调器（R1-2）：框架以配置为期望态、diff 实际态后
// 收敛；单个协调器失败仅记日志，由下次协调补齐，不阻断整机。
func Reconcile(name string, fn appkit.ReconcileFunc) appkit.Option {
	return reconcileOption{name: name, fn: fn}
}
