// register.go 是机制层的导出注册面：AppKit 上的 Add*/Set* 方法把各机制的
// 装配期状态写入路径收敛为导出方法，使 pkg/appkit/mech 能在单向依赖
// （mech → appkit，appkit 不 import mech）下构造 appkit.Option。
//
// 语义约束：每个方法体与对应 Option 构造函数的原写入路径逐字段一致
// （同一 append/赋值、无锁变化）；appkit 的机制构造函数已改为一行薄转发到
// 这里，mech 的同名构造器经同一方法写入——两条装配入口共享唯一写入路径。
// 运行期登记（addEffect 等持锁路径）不属于本面，保持原样。
package appkit

import (
	"context"
	"time"
)

// AddProvides 追加能力声明（Provides Option 的写入路径，见 capability.go）。
func (a *AppKit) AddProvides(caps ...string) { a.provides = append(a.provides, caps...) }

// AddRequires 追加组件能力依赖声明（Requires Option 的写入路径）。
func (a *AppKit) AddRequires(component string, caps ...string) {
	a.requires = append(a.requires, requirement{component: component, caps: caps})
}

// AddEffect 装配期登记一条可逆效应（Effect Option 的写入路径）。运行期登记
// 走 addEffect（持 effectsMu）——装配期 Option 单线程遍历直写、运行期并发
// 追加持锁，两条路径语义不同，保持各自原样。
func (a *AppKit) AddEffect(name string, undo func(context.Context) error) {
	a.effects = append(a.effects, effectEntry{name: name, undo: undo})
}

// SetEffectTimeout 设置效应撤销阶段的独立超时（EffectTimeout Option 的写入路径）。
func (a *AppKit) SetEffectTimeout(d time.Duration) { a.effectTimeout = d }

// AddComponents 追加进程内组件（Components Option 的写入路径，见 component.go）。
func (a *AppKit) AddComponents(comps ...Component) { a.components = append(a.components, comps...) }

// SetComponentTimeout 设置组件 Start/Dispose 的独立超时（ComponentTimeout Option 的写入路径）。
func (a *AppKit) SetComponentTimeout(d time.Duration) { a.componentTimeout = d }

// AddKeyWatch 追加 key 级配置订阅（OnKeyChange Option 的写入路径，见 keywatch.go）。
func (a *AppKit) AddKeyWatch(key string, fn func(old, new string)) {
	a.keyWatchers = append(a.keyWatchers, keyWatcher{key: key, fn: fn})
}

// AddReconcile 追加期望态协调器（Reconcile Option 的写入路径，见 reconcile.go）。
func (a *AppKit) AddReconcile(name string, fn ReconcileFunc) {
	a.reconcilers = append(a.reconcilers, reconciler{name: name, fn: fn})
}
