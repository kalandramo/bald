// reconcile.go 实现 R1 第二子集：期望态 diff-apply 协调器
// （docs/devel/zh-CN/架构优化路线.md §3 R1，对照 Cordis 组件加载器的增量协调
// 与 Kubernetes 的 declarative reconcile 循环）。
//
// R1-1（OnKeyChange）是**事件驱动**：框架告诉你「key 从 A 变 B」，业务自己决定
// 怎么响应。R1-2 是**状态驱动**：业务只声明「期望是什么样」，框架拿期望态与实际态
// 做差集，只增/删/改差异部分，反复执行直到收敛。
//
// 核心差异是**收敛性**：事件模型下连续变更/部分失败/乱序会留下中间态，业务回调
// 要自己兜底；状态模型下每次协调都重读两边再 diff，失败不回滚、下次重试自然补齐
// ——即 Kubernetes controller 挂了重启也能继续收敛的原理。
//
// 典型用法（审计后端热切换，无需重启）：
//
//	app := appkit.New(
//	    appkit.Reconcile("audit.backends", func(ctx context.Context, r *appkit.ReconcileCtx) error {
//	        want := parseBackends(r.Viper.GetStringSlice("audit.backends")) // 期望态
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
package appkit

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/viper"

	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/log"
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

// ReconcileCtx 是协调函数的上下文：暴露期望态读取（Viper）与实际态操作（挂载集合）。
type ReconcileCtx struct {
	// Viper 读取期望态（配置）；只读，且为当前已合并的最新配置实例。
	Viper *viper.Viper

	// Name 是本协调器的注册名（日志/审计定位用）。
	Name string

	app *AppKit
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
// 语义与 AppKit.MountComponent 一致（Start + 纳入停机序列 + mount 审计事件）。
func (r *ReconcileCtx) Mount(ctx context.Context, name string, c Component) error {
	return r.app.reconMount(ctx, r.Name, name, c)
}

// Unmount 卸载本协调器名下的组件（移除 + Dispose + unmount 审计事件）。
func (r *ReconcileCtx) Unmount(ctx context.Context, name string) error {
	return r.app.reconUnmount(ctx, r.Name, name)
}

// Reconcile 注册一个期望态协调器（R1-2）。多个协调器按注册序依次执行；
// 单个协调器返回 error 时其余继续（隔离），错误仅记日志——协调是收敛过程，
// 失败由下次协调补齐，不因单点失败阻断整机。
func Reconcile(name string, fn ReconcileFunc) Option {
	return func(a *AppKit) {
		a.reconcilers = append(a.reconcilers, reconciler{name: name, fn: fn})
	}
}

// ReconcileNow 立即执行一次全量协调（按注册序）。供测试与「配置变更后手动触发」
// 使用；配置 watch 触发路径见 runReconcilers（经 OnConfigChange 包裹）。
func (a *AppKit) ReconcileNow(ctx context.Context) {
	a.runReconcilers(ctx, a.cfg.v)
}

// runReconcilers 顺序执行全部协调器，单个失败仅记日志（收敛语义：下次补齐）。
func (a *AppKit) runReconcilers(ctx context.Context, v *viper.Viper) {
	a.reconMu.Lock()
	rs := append([]reconciler(nil), a.reconcilers...)
	a.reconMu.Unlock()

	for _, rc := range rs {
		r := &ReconcileCtx{Viper: v, Name: rc.name, app: a}
		err := rc.fn(ctx, r)
		if err != nil {
			log.GetLogger().Error(ctx, "appkit reconcile failed", "reconciler", rc.name, "error", err)
		}
		// 成功与失败都留审计痕迹（收敛语义：失败也是可观测状态，下次补齐）。
		a.auditReconcile(ctx, "reconcile", rc.name, err)
	}
}

// ---- 协调器归属跟踪（实际态） ----

// reconMounted 返回协调器 name 名下已挂载的组件名（挂载序）。
func (a *AppKit) reconMounted(recon string) []string {
	a.reconMu.Lock()
	defer a.reconMu.Unlock()
	names := make([]string, 0, len(a.reconItems[recon]))
	for _, it := range a.reconItems[recon] {
		names = append(names, it.name)
	}
	return names
}

// reconGet 取协调器名下的组件实例。
func (a *AppKit) reconGet(recon, name string) (Component, bool) {
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
func (a *AppKit) reconMount(ctx context.Context, recon, name string, c Component) error {
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
func (a *AppKit) reconUnmount(ctx context.Context, recon, name string) error {
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

// auditReconcile 旁路发出协调审计事件（与 A1 挂载/卸载同源，Object="reconciler"）。
func (a *AppKit) auditReconcile(ctx context.Context, action, name string, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.GetLogger().Error(ctx, "appkit reconcile audit panicked", "panic", r)
		}
	}()
	subject := ""
	if claims := authn.AuthClaimsFromContext(ctx); claims != nil {
		subject = claims.Subject
	}
	ev := audit.AuditEvent{
		Subject: subject,
		Object:  "reconciler",
		Action:  action,
		Result:  audit.ResultAllow,
		Meta:    map[string]any{"reconciler": name},
	}
	if err != nil {
		ev.Result = audit.ResultError
		ev.Error = err.Error()
	}
	audit.GetAuditor().Record(ctx, ev)
}

// DiffStrings 是协调常用工具：返回期望集与实际集的差集（新增 / 移除，均有序）。
// 便于业务写「期望有实际无→挂载、实际有期望无→卸载」的协调逻辑：
//
//	add, remove := appkit.DiffStrings(want, have)
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

// ParseStringList 把配置值解析为字符串列表：支持 viper 原生 []string，以及
// 逗号分隔的字符串（"log,store,stream"）；空值返回 nil。
func ParseStringList(v any) []string {
	switch val := v.(type) {
	case nil:
		return nil
	case []string:
		out := make([]string, 0, len(val))
		for _, s := range val {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(val) == "" {
			return nil
		}
		parts := strings.Split(val, ",")
		out := make([]string, 0, len(parts))
		for _, s := range parts {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}

// reconItem 是协调器归属表条目。
type reconItem struct {
	name string
	comp Component
}
