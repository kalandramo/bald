// mount.go 实现 A1 运行时可逆挂载（docs/devel/zh-CN/架构优化路线.md §6，
// agent-native 最小前置，同时是 R1 期望态 diff-apply 的底层支撑）。
//
// 设计（对照论文时间可组合性在运行期的延伸）：
//   - Registry[T].Mount/Unmount：装配期 Register（不可逆、冲突报错，init() 自注册
//     语义）的**运行期对偶**——可逆挂载、undo 幂等。两个语义必须分开：装配期错误
//     应 fail-fast（panic 合理），运行期变更必须可回滚（效应账本语义）。
//   - AppKit.MountComponent/UnmountComponent：把 C1 组件生命周期扩展到运行期——
//     挂载即 Start 并纳入停机序列（stopAll 阶段 4 逆序 Dispose），卸载即移除并
//     立即 Dispose。管理面热插拔、agent 工具挂载共用此原语。
//   - 运行时重组天然一条审计事件（§6.2 自修改审计）：AuditEvent{Object:"component",
//     Action:"mount"/"unmount", Subject:ctx 中的管理面身份}，旁路不阻断。
//
// 典型管理面用法：
//
//	func mountHandler(app *appkit.AppKit) gin.HandlerFunc {
//	    return func(c *gin.Context) {
//	        comp := newComponentFromRequest(c)
//	        if err := app.MountComponent(c.Request.Context(), comp); err != nil {
//	            c.JSON(500, ...); return
//	        }
//	        // 请求 ctx 已带 AuthClaims → 审计事件自动携带 subject
//	    }
//	}
package appkit

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/log"
)

// ErrNotRunning 表示 App 尚未运行（Run 之前）或已退出——运行期原语仅可在
// Run 存活期间使用。
var ErrNotRunning = errors.New("appkit: app not running")

// errComponentsDisposed 表示组件系统已进入停机销毁（stopAll 阶段 4 执行后），
// 不再接受新的挂载。
var errComponentsDisposed = errors.New("appkit: components already disposed")

// ---- Registry[T]：运行期可逆挂载 ----

// Mount 运行期可逆挂载：与 Register（装配期、不可逆、冲突报错）对偶。
// 挂载成功返回 undo 闭包（幂等：重复调用无副作用）；已存在同名条目（无论
// Register 还是 Mount 写入）返回错误，不做覆盖。
//
// 约定：同名字换实现应先 Unmount 旧条目再 Mount 新条目（mount/unmount 配对）。
// undo 语义为「撤销本次挂载」：若违反约定在同名新条目存在时执行旧 undo，会按名
// 移除（此时移除的是新条目）——管理面调用保持配对即可避免。
func (r *Registry[T]) Mount(name string, impl T) (undo func(), err error) {
	r.mu.Lock()
	if _, ok := r.items[name]; ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("appkit: %q already registered", name)
	}
	r.items[name] = impl
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { _, _ = r.Unmount(name) })
	}, nil
}

// Unmount 按名卸载（可逆注销），返回被卸载的实例；不存在时返回零值与 false。
func (r *Registry[T]) Unmount(name string) (T, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	impl, ok := r.items[name]
	if !ok {
		var zero T
		return zero, false
	}
	delete(r.items, name)
	return impl, true
}

// ---- AppKit：运行期组件热插拔 ----

// MountComponent 运行中动态挂载组件：立即 Start 并纳入停机序列（stopAll 阶段 4
// 逆序 Dispose，顺序=挂载序）。管理面热插拔/agent 工具挂载的承载原语；装配期
// 静态组件用 Components Option。
//
// 产生一条 mount 审计事件（Subject 自动取 ctx 中的 AuthClaims——管理面 handler
// 传入带认证的 request ctx 即可带上身份），审计旁路不阻断挂载。
//
// 竞态闭环：组件系统停机销毁（阶段 4）开始后拒绝新挂载；若销毁与挂载并发竞争，
// 挂载方的 Start 会随之回滚 Dispose——保证「挂载失败 = 无副作用」。
func (a *AppKit) MountComponent(ctx context.Context, c Component) error {
	if !a.running.Load() {
		return ErrNotRunning
	}
	if c == nil {
		return errors.New("appkit: MountComponent: nil component")
	}
	if err := a.runHook(ctx, a.componentTimeout, "component:"+c.Name()+":start", c.Start); err != nil {
		return fmt.Errorf("appkit: component %q start: %w", c.Name(), err)
	}

	a.startedMu.Lock()
	if a.disposed {
		a.startedMu.Unlock()
		// 组件系统已销毁：回滚本次 Start，保持挂载失败=无副作用。
		_ = a.runHook(ctx, a.componentTimeout, "component:"+c.Name()+":rollback", c.Dispose)
		return errComponentsDisposed
	}
	a.started = append(a.started, c)
	a.startedMu.Unlock()

	log.GetLogger().Info(ctx, "appkit component mounted", "component", c.Name())
	a.auditComponent(ctx, "mount", c.Name())
	return nil
}

// UnmountComponent 运行中卸载组件：从停机序列移除并立即 Dispose（此后 stopAll
// 不再触碰）。卸载最新挂载的同名条目（LIFO）；未挂载返回错误。
// Dispose 失败仅记日志——组件已从序列移除，维持卸载结果（旁路语义）。
func (a *AppKit) UnmountComponent(ctx context.Context, name string) error {
	if !a.running.Load() {
		return ErrNotRunning
	}
	a.startedMu.Lock()
	idx := -1
	for i := len(a.started) - 1; i >= 0; i-- {
		if a.started[i].Name() == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		a.startedMu.Unlock()
		return fmt.Errorf("appkit: component %q not mounted", name)
	}
	c := a.started[idx]
	a.started = append(a.started[:idx], a.started[idx+1:]...)
	a.startedMu.Unlock()

	if err := a.runHook(ctx, a.componentTimeout, "component:"+name+":dispose", c.Dispose); err != nil {
		log.GetLogger().Error(ctx, "appkit component unmount dispose failed", "component", name, "error", err)
	}
	log.GetLogger().Info(ctx, "appkit component unmounted", "component", name)
	a.auditComponent(ctx, "unmount", name)
	return nil
}

// ListComponents 返回当前纳入停机序列的组件名（装配期 + 运行期挂载的合集，
// 按启动/挂载序排列），供管理面观测端点使用。
func (a *AppKit) ListComponents() []string {
	a.startedMu.Lock()
	defer a.startedMu.Unlock()
	names := make([]string, 0, len(a.started))
	for _, c := range a.started {
		names = append(names, c.Name())
	}
	return names
}

// resolveAuditor 返回 appkit 自身审计事件的接收后端：显式注入（Auditor Option）
// 优先，否则回退全局（其默认即 nop，永不返回 nil）。D4：单一解析顺序，消除
// bundle 注入与 appkit 全局各走一边的 split-brain sink。
func (a *AppKit) resolveAuditor() audit.Auditor {
	if a.auditor != nil {
		return a.auditor
	}
	return audit.GetAuditor()
}

// auditComponent 旁路发出一条运行时重组审计事件（§6.2 自修改审计原语）：
// Subject/TenantID 取 ctx 中的 AuthClaims（可为空）；Auditor panic/错误仅记日志，
// 绝不阻断挂载/卸载本身（与审计中间件 recordSafely 同一纪律）。
func (a *AppKit) auditComponent(ctx context.Context, action, name string) {
	defer func() {
		if r := recover(); r != nil {
			log.GetLogger().Error(ctx, "appkit component audit panicked", "panic", r)
		}
	}()
	subject, tenant := "", ""
	if claims := authn.AuthClaimsFromContext(ctx); claims != nil {
		subject, tenant = claims.Subject, claims.TenantID
	}
	a.resolveAuditor().Record(ctx, audit.AuditEvent{
		Subject:  subject,
		TenantID: tenant,
		Object:   "component",
		Action:   action,
		Result:   audit.ResultAllow,
		Meta:     map[string]any{"component": name},
	})
}
