// component.go 实现 C1 Server ⊂ Component（docs/devel/zh-CN/架构优化路线.md）。
//
// 背景（对照 Cordis 论文「万物皆插件」的 bald 可翻译子集）：trace provider、
// metrics exporter、审计后端、缓存连接池等进程内基础设施此前是散装全局
// （Setup + 手工 BeforeStop flush），忘挂 BeforeStop 就丢数据（M9：trace 退出前
// 必须 shutdown 否则丢尾批 span）。C1 把它们统一为有生命周期的组件，与 servers
// 一起纳入 appkit 编排：顺序 Start（注册序=依赖建立序）、逆序 Dispose（停机
// 对称）、panic 隔离、独立超时——「trace 忘 flush 丢批」从文档知识变成结构保证。
//
// Component 与 T1 效应账本互补：账本管全局注册表的逆操作（低级、细粒度），
// 组件管实例生命周期（高级、结构化）。典型用法：
//
//	traceShutdown, _ := trace.Setup(...)
//	app := appkit.New(
//	    appkit.Components(appkit.ComponentFunc("trace.provider", traceShutdown)),
//	    ...
//	)
package appkit

import (
	"context"
	"fmt"
	"time"

	"github.com/kalandramo/bald/pkg/log"
)

// Component 是带生命周期的进程内基础设施（对照 server.Server 的广义化：
// Server = Component + Endpoint）。实现可为 tracing provider、metrics exporter、
// 审计后端连接、缓存池等。
type Component interface {
	// Name 组件名（日志与报错定位），建议 "<domain>.<name>"。
	Name() string
	// Start 建立组件（beforeStart 钩子之后、servers 启动之前，顺序执行——
	// 注册序即依赖建立序）。
	Start(ctx context.Context) error
	// Dispose 释放组件（stopAll 最后一阶段，AfterStop 之后，逆序执行）。
	// 语义与 T1 效应的 undo 对称：调用后组件对共享环境的影响应完全撤销。
	Dispose(ctx context.Context) error
}

// ComponentFunc 把一个无启动逻辑的清理函数（如 otel TracerProvider.Shutdown、
// database/sql Close）适配为 Component。
func ComponentFunc(name string, dispose func(context.Context) error) Component {
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

// Components 注册进程内组件（可多次调用，追加）。
func Components(comps ...Component) Option {
	return func(a *AppKit) { a.components = append(a.components, comps...) }
}

// ComponentTimeout 设置组件 Start/Dispose 的独立超时（默认 defaultHookTimeout）。
func ComponentTimeout(d time.Duration) Option {
	return func(a *AppKit) { a.componentTimeout = d }
}

// startComponents 顺序启动全部组件；任一失败则由调用方经 stopAll 逆序 Dispose
// 已启动组件（started 跟踪保证只处理成功 Start 过的，幂等）。
func (a *AppKit) startComponents(ctx context.Context) error {
	for _, c := range a.components {
		if err := a.runHook(ctx, a.componentTimeout, "component:"+c.Name()+":start", c.Start); err != nil {
			return fmt.Errorf("appkit: component %q start: %w", c.Name(), err)
		}
		a.startedMu.Lock()
		a.started = append(a.started, c)
		a.startedMu.Unlock()
		log.GetLogger().Info(ctx, "appkit component started", "component", c.Name())
	}
	return nil
}

// disposeComponents 逆序 Dispose 全部已启动组件（幂等：处理完清空 started，
// 重复调用无副作用）。单个组件超时或 panic 仅记日志，不中断其余销毁（旁路语义，
// 与审计/效应撤销一致）。
//
// A1 竞态闭环：置位 disposed（锁内）后才快照销毁——此后 MountComponent 被
// 拒绝（并回滚其 Start），保证销毁后不再有组件逃逸出停机序列。
func (a *AppKit) disposeComponents(parent context.Context) {
	a.startedMu.Lock()
	if a.disposed {
		a.startedMu.Unlock()
		return
	}
	a.disposed = true
	started := a.started
	a.started = nil
	a.startedMu.Unlock()

	for i := len(started) - 1; i >= 0; i-- {
		c := started[i]
		if err := a.runHook(parent, a.componentTimeout, "component:"+c.Name()+":dispose", c.Dispose); err != nil {
			log.GetLogger().Error(parent, "appkit component dispose failed", "component", c.Name(), "error", err)
		}
	}
}
