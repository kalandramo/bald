// component.go 实现 C1 Server ⊂ Component（docs/devel/zh-CN/架构优化路线.md）。
//
// 背景（对照 Cordis 论文「万物皆插件」的 bald 可翻译子集）：trace provider、
// metrics exporter、审计后端、缓存连接池等进程内基础设施此前是散装全局
// （Setup + 手工 BeforeStop flush），忘挂 BeforeStop 就丢数据（trace 退出前
// 必须 shutdown 否则丢尾批 span）。C1 把它们统一为有生命周期的组件，与 servers
// 一起纳入 appkit 编排：顺序 Start（注册序=依赖建立序）、逆序 Dispose（停机
// 对称）、panic 隔离、独立超时——「trace 忘 flush 丢批」从文档知识变成结构保证。
//
// Component 与 T1 效应账本互补：账本管全局注册表的逆操作（低级、细粒度），
// 组件管实例生命周期（高级、结构化）。典型用法：
//
//	traceShutdown, _ := trace.Setup(...)
//	app := appkit.NewApp(
//	    mech.Components(mech.ComponentFunc("trace.provider", traceShutdown)),
//	    ...
//	)
//
// 导出构造面在 pkg/appkit/mech（mech.Components/mech.ComponentFunc/
// mech.ComponentTimeout，B5 下沉）；本文件的未导出 componentsOpt/
// componentTimeoutOpt 是引擎透传与包内测试共用的写入面。Component 接口与
// 引擎（startComponents/disposeComponents/MountComponent）归 appkit——
// Decl.Components 的消费方，且 mech 单向 import appkit。
package appkit

import (
	"context"
	"fmt"
	"time"

	"github.com/kalandramo/bald/log"
)

// Component 是带生命周期的进程内基础设施（对照 transport.Server 的广义化：
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

// componentsOpt 注册进程内组件（mech.Components 的引擎内部写入面；可多次
// 调用，追加）。
func componentsOpt(comps ...Component) Option {
	return optionFunc(func(d *Decl) { d.Components = append(d.Components, comps...) })
}

// componentTimeoutOpt 设置组件 Start/Dispose 的独立超时（默认 defaultHookTimeout）。
func componentTimeoutOpt(d time.Duration) Option {
	return optionFunc(func(decl *Decl) { decl.ComponentTimeout = d })
}

// wrapComponentStartErr 统一组件启动失败的错误包裹（startComponents 与
// MountComponent 共用——E11 收敛，错误文案单一真相源）。
func wrapComponentStartErr(name string, err error) error {
	return fmt.Errorf("appkit: component %q start: %w", name, err)
}

// startComponents 顺序启动全部组件；任一失败则由调用方经 stopAll 逆序 Dispose
// 已启动组件（started 跟踪保证只处理成功 Start 过的，幂等）。
func (a *App) startComponents(ctx context.Context) error {
	for _, c := range a.components {
		if err := a.runHook(ctx, a.componentTimeout, "component:"+c.Name()+":start", c.Start); err != nil {
			return wrapComponentStartErr(c.Name(), err)
		}
		a.startedMu.Lock()
		a.started = append(a.started, c)
		a.startedMu.Unlock()
		log.Info(ctx, "appkit component started", "component", c.Name())
	}
	return nil
}

// disposeComponents 逆序 Dispose 全部已启动组件（幂等：处理完清空 started，
// 重复调用无副作用）。单个组件超时或 panic 仅记日志，不中断其余销毁（旁路语义，
// 与审计/效应撤销一致）。
//
// A1 竞态闭环：置位 disposed（锁内）后才快照销毁——此后 MountComponent 被
// 拒绝（并回滚其 Start），保证销毁后不再有组件逃逸出停机序列。
func (a *App) disposeComponents(parent context.Context) {
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
			log.Error(parent, "appkit component dispose failed", "component", c.Name(), "error", err)
		}
	}
}
