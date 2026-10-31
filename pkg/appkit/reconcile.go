// reconcile.go 实现期望态 diff-apply 协调器
// （见 docs/devel/zh-CN/应用框架设计.md；与 Kubernetes declarative
// reconcile 循环同型）。
//
// key 级订阅（OnKeyChange）是**事件驱动**：框架告诉你「key 从 A 变 B」，业务自己决定
// 怎么响应。协调器是**状态驱动**：业务只声明「期望是什么样」，框架拿期望态与实际态
// 做差集，只增/删/改差异部分，反复执行直到收敛。
//
// 核心差异是**收敛性**：事件模型下连续变更/部分失败/乱序会留下中间态，业务回调
// 要自己兜底；状态模型下每次协调都重读两边再 diff，失败不回滚、下次重试自然补齐
// ——即 Kubernetes controller 挂了重启也能继续收敛的原理。
//
// 典型用法（审计后端热切换，无需重启）：
//
//	app := appkit.NewApp(
//	    mech.Reconcile("audit.backends", func(ctx context.Context, r *appkit.ReconcileCtx) error {
//	        want := parseBackends(r.StringSlice("audit.backends")) // 期望态
//	        // 新增：期望有、实际无
//	        for _, name := range want {
//	            if !r.Has(name) {
//	                if err := r.Mount(ctx, name, factory(name)); err != nil {
//	                    return err // 失败不回滚，下次协调继续收敛
//	                }
//	            }
//	        }
//	        // 移除：实际有、期望无
//	        for _, name := range r.Mounted() {
//	            if !wantSet[name] {
//	                if err := r.Unmount(ctx, name); err != nil {
//	                    return err
//	                }
//	            }
//	        }
//	        return nil
//	    }),
//	    ...
//	)
//
// 导出构造面在 pkg/appkit/mech（mech.Reconcile，B5 下沉）；本文件的未导出
// reconcileOpt 是引擎透传与包内测试共用的写入面。ReconcileCtx/ReconcileFunc
// 与协调引擎（runReconcilers 等）归 appkit——接收者方法与引擎私有状态。
package appkit

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/bootstrap/config"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"
)

// ReconcileFunc 执行一次协调：读期望态（经 r.Viper）、比对实际态（r.Mounted/r.Has）、
// 调 r.Mount/r.Unmount 收敛。必须是幂等的：任意时刻重复调用都朝同一终态收敛，
// 且中间失败后再次调用能继续补齐。
type ReconcileFunc func(ctx context.Context, r *ReconcileCtx) error

// reconciler 是一个期望态协调器。
type reconciler struct {
	name string
	fn   ReconcileFunc
}

// ReconcileCtx 是协调函数的上下文：暴露期望态读取（Settings）与实际态操作（挂载集合）。
type ReconcileCtx struct {
	// Settings 是期望态（配置）：本次协调开始时的配置快照（深拷贝，只读）。
	// 一次协调内取值一致；跨协调由框架取最新快照。
	Settings map[string]any

	// Name 是本协调器的注册名（日志/审计定位用）。
	Name string

	app *App
}

// String 读取期望态中点路径的字符串值（缺失/nil → ""，标量 → 字符串化）。
// 薄委托 config.At + config.String（点路径读取与标量字符串化是
// 配置装载层的值语义，实现体归 bootstrap/config，appkit 只留接收者方法）。
func (r *ReconcileCtx) String(key string) string {
	v, ok := config.At(r.Settings, key)
	if !ok {
		return ""
	}
	return config.String(v)
}

// StringSlice 读取期望态中点路径的字符串列表：支持 []string、[]any（yaml 列表）
// 与逗号分隔的字符串；缺失/空值返回 nil。薄委托 config.StringList（语义与
// 之完全一致——ParseStringList 的实现体已随职责边界落点迁出 appkit）。
func (r *ReconcileCtx) StringSlice(key string) []string {
	v, ok := config.At(r.Settings, key)
	if !ok {
		return nil
	}
	return config.StringList(v)
}

// Mounted 返回当前实际已挂载、由本协调器管理的组件名（有序，与挂载序一致）。
// 用于「实际有、期望无」的移除判定。
func (r *ReconcileCtx) Mounted() []string {
	return r.app.reconMounted(r.Name)
}

// Has 判断本协调器名下是否已挂载 name。
func (r *ReconcileCtx) Has(name string) bool {
	_, ok := r.app.reconGet(r.Name, name)
	return ok
}

// Mount 挂载一个由本协调器管理的组件（记入归属，供下次 diff）。
// 语义与 App.MountComponent 一致（Start + 纳入停机序列 + mount 审计事件）。
func (r *ReconcileCtx) Mount(ctx context.Context, name string, c Component) error {
	return r.app.reconMount(ctx, r.Name, name, c)
}

// Unmount 卸载本协调器名下的组件（移除 + Dispose + unmount 审计事件）。
func (r *ReconcileCtx) Unmount(ctx context.Context, name string) error {
	return r.app.reconUnmount(ctx, r.Name, name)
}

// NewReconcileCtx 构造一个协调上下文，供测试或「手动触发协调」使用（正常路径下
// 由 runReconcilers 内部构造，无需调用方关心）。测试装置用 NewHarness 构造
// 运行态 App 后，配合本函数显式注入期望态快照驱动协调逻辑（见 harness.go）。
func NewReconcileCtx(app *App, name string, settings map[string]any) *ReconcileCtx {
	return &ReconcileCtx{Settings: settings, Name: name, app: app}
}

// reconcileOpt 注册一个期望态协调器（mech.Reconcile 的引擎内部写入面）。
// 多个协调器按注册序依次执行；单个协调器返回 error 时其余继续（隔离），
// 错误仅记日志——协调是收敛过程，失败由下次协调补齐，不因单点失败阻断整机。
func reconcileOpt(name string, fn ReconcileFunc) Option {
	return optionFunc(func(d *Decl) { d.Reconciles = append(d.Reconciles, ReconcileDecl{Name: name, Fn: fn}) })
}

// ReconcileNow 立即执行一次全量协调（按注册序）。供测试与「配置变更后手动触发」
// 使用；配置 watch 触发路径见 runReconcilers（经 OnConfigChange 包裹）。
func (a *App) ReconcileNow(ctx context.Context) {
	a.runReconcilers(ctx)
}

// runReconcilers 顺序执行全部协调器，单个失败仅记日志（收敛语义：下次补齐）。
// 每次协调整体取一次配置快照，保证单个协调内期望态一致。
func (a *App) runReconcilers(ctx context.Context) {
	var settings map[string]any
	if a.cfg.store != nil {
		settings = a.cfg.store.Settings()
	}
	a.runReconcilersWith(ctx, settings)
}

// runReconcilersWith 以给定期望态快照执行协调（测试/手动注入用；正常路径走
// runReconcilers 由配置仓库取快照）。
func (a *App) runReconcilersWith(ctx context.Context, settings map[string]any) {
	a.reconMu.Lock()
	rs := append([]reconciler(nil), a.reconcilers...)
	a.reconMu.Unlock()

	for _, rc := range rs {
		r := &ReconcileCtx{Settings: settings, Name: rc.name, app: a}
		err := rc.fn(ctx, r)
		if err != nil {
			log.Error(ctx, "appkit reconcile failed", "reconciler", rc.name, "error", err)
		}
		// 成功与失败都留审计痕迹（收敛语义：失败也是可观测状态，下次补齐）。
		a.auditReconcile(ctx, "reconcile", rc.name, err)
	}
}

// ---- 协调器归属跟踪（实际态） ----

// reconMounted 返回协调器 name 名下已挂载的组件名（挂载序）。
func (a *App) reconMounted(recon string) []string {
	a.reconMu.Lock()
	defer a.reconMu.Unlock()
	names := make([]string, 0, len(a.reconItems[recon]))
	for _, it := range a.reconItems[recon] {
		names = append(names, it.name)
	}
	return names
}

// reconGet 取协调器名下的组件实例。
func (a *App) reconGet(recon, name string) (Component, bool) {
	a.reconMu.Lock()
	defer a.reconMu.Unlock()
	for _, it := range a.reconItems[recon] {
		if it.name == name {
			return it.comp, true
		}
	}
	return nil, false
}

// reconMount 挂载并记入协调器归属（幂等：同名已挂载返回错误，避免重复）。
func (a *App) reconMount(ctx context.Context, recon, name string, c Component) error {
	if c == nil {
		return fmt.Errorf("appkit: reconcile mount %s: nil component", name)
	}
	// 先挂载（Start + 审计），成功后才记入归属——保证归属表与实际挂载一致。
	if err := a.MountComponent(ctx, c); err != nil {
		return fmt.Errorf("appkit: reconcile mount %s: %w", name, err)
	}
	a.reconMu.Lock()
	a.reconItems[recon] = append(a.reconItems[recon], reconItem{name: name, comp: c})
	a.reconMu.Unlock()
	return nil
}

// reconUnmount 卸载并移除归属（幂等：不存在返回错误）。
func (a *App) reconUnmount(ctx context.Context, recon, name string) error {
	a.reconMu.Lock()
	items := a.reconItems[recon]
	idx := -1
	for i, it := range items {
		if it.name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		a.reconMu.Unlock()
		return fmt.Errorf("appkit: reconcile unmount %s: not mounted", name)
	}
	a.reconItems[recon] = append(items[:idx:idx], items[idx+1:]...)
	a.reconMu.Unlock()

	// 卸载失败不恢复归属（组件已 Dispose 或未挂载）；下次协调按实际态重新判定。
	return a.UnmountComponent(ctx, name)
}

// auditReconcile 旁路发出协调审计事件（与运行期挂载/卸载同源，Object="reconciler"；
// 后端经 D4 resolveAuditor 单一解析，与请求级审计同源）。
func (a *App) auditReconcile(ctx context.Context, action, name string, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Error(ctx, "appkit reconcile audit panicked", "panic", r)
		}
	}()
	subject, tenant := "", ""
	if a.identity != nil {
		subject, tenant = a.identity(ctx)
	}
	ev := audit.AuditEvent{
		Subject:  subject,
		TenantID: tenant,
		Object:   "reconciler",
		Action:   action,
		Result:   audit.ResultAllow,
		Meta:     map[string]any{"reconciler": name},
	}
	if err != nil {
		ev.Result = audit.ResultError
		ev.Error = err.Error()
	}
	a.resolveAuditor().Record(ctx, ev)
}

// reconItem 是协调器归属表条目。
type reconItem struct {
	name string
	comp Component
}
