// effect.go 实现效应账本（见 docs/devel/zh-CN/应用框架设计.md）。
//
// 背景：bald 的全局注册点（store.RegisterTenant / log.SetLogger 等进程级
// 注册点）若没有逆操作记录，对共享环境的写入就是不可逆效应——覆盖即丢弃旧值。
// 进程级优雅停机（三阶段）已经落地，但组件级（进程内）的「卸载零残骸」
// 仍缺失，导致：e2e 测试全局污染、重复注册语义靠约定（InitBridges 幂等）。
//
// 效应账本把这些约定变成结构保证：任何全局写入配套登记一条逆操作（undo），
// 停机（或启动失败回滚）时逆序回放，系统恢复到装配前的干净状态
// （导出构造面见 pkg/appkit/mech 的 mech.Effect）：
//
//	old := log.GetLogger()
//	log.SetLogger(myLogger)
//	app := appkit.NewApp(
//	    mech.Effect("my-logger", func(ctx context.Context) error {
//	        log.SetLogger(old) // 恢复旧值
//	        return nil
//	    }),
//	    ...
//	)
//
// e2e 测试隔离用法（免跑完整 Run）：
//
//	app := appkit.NewApp(mech.Effect(...))
//	t.Cleanup(func() { app.UndoEffects(context.Background()) })
package appkit

import (
	"context"
	"time"

	"github.com/kalandramo/bald/log"
)

// effectEntry 是账本中的一条可逆效应：name 用于日志定位，undo 是逆操作。
// deferred 标记分层（AR-1）：false = 全局写入撤销（stopAll 阶段 0，先于
// BeforeStop）；true = 资源回收类（stopAll 阶段 5，Dispose 之后——server
// 排空与钩子期间组件依赖的资源必须仍然可用）。
type effectEntry struct {
	name     string
	undo     func(ctx context.Context) error
	deferred bool
}

// effectOpt 登记一条可逆效应（mech.Effect 的引擎内部写入面，
// appkit 侧不导出）。undo 在停机（stopAll 阶段 0，先于 BeforeStop 钩子）
// 或启动失败回滚时**逆序**执行（后注册的先撤销，与依赖建立顺序对称）。
//
// 契约（由 effect_test.go 固化）：
//   - undo 每条独立超时（effectTimeoutOpt，未声明时 = 阶段预算 stopTimeout/2）；
//   - undo panic 被隔离，不拖垮其余撤销与整机停机；
//   - 回放幂等：执行后账本清空，重复调用无副作用。
func effectOpt(name string, undo func(ctx context.Context) error) Option {
	return optionFunc(func(d *Decl) { d.Effects = append(d.Effects, EffectDecl{Name: name, Undo: undo}) })
}

// deferredEffectOpt 登记一条**资源回收类**效应（AR-1 分层）：它在 stopAll
// 阶段 5（Dispose 之后、logger 恢复收尾）逆序回放，而不是阶段 0——provider
// 的 DB/缓存/注册中心 cleanup 若在阶段 0 兑现，排空中的请求与 BeforeStop
// 钩子拿到的就是已关闭的资源。回放超时/panic 隔离与 effectOpt 相同。
func deferredEffectOpt(name string, undo func(ctx context.Context) error) Option {
	return optionFunc(func(d *Decl) {
		d.Effects = append(d.Effects, EffectDecl{Name: name, Undo: undo, Deferred: true})
	})
}

// effectTimeoutOpt 设置效应撤销阶段的独立超时（每条 undo 各享此超时）。
func effectTimeoutOpt(d time.Duration) Option {
	return optionFunc(func(decl *Decl) { decl.EffectTimeout = d })
}

// addEffect 运行期登记一条逆操作（幂等追加，并发安全）。
//
// 与 Option 版 Effect 的区别：Effect 在**装配期**（选项遍历时）登记；本方法
// 供运行期钩子登记——典型场景是 Run 期才发生的服务器构造：构造产物
// （cleanup）只能在构造发生的同一时刻入账，否则「构造成功但注册失败」的
// 窗口会泄漏资源。
func (a *App) addEffect(name string, undo func(ctx context.Context) error) {
	a.effectsMu.Lock()
	a.effects = append(a.effects, effectEntry{name: name, undo: undo})
	a.effectsMu.Unlock()
}

// UndoEffects 立即逆序回放全部已登记效应（含资源回收类），并清空账本（幂等）。
// 正常路径无需手动调用（Run 停机会按层自动回放）；主要供启动失败回滚与 e2e
// 测试隔离使用——这两类场景要的是「恢复到装配前」的完整回滚，不分层：
//
//	t.Cleanup(func() { app.UndoEffects(context.Background()) })
//
// 单条 undo 超时或 panic 仅记日志，不中断其余回放（旁路语义，与审计一致）。
func (a *App) UndoEffects(parent context.Context) {
	a.undoEffectsWhere(parent, false)
	a.undoEffectsWhere(parent, true)
}

// undoGlobalEffects 只回放全局写入类效应（stopAll 阶段 0，AR-1 分层）：
// config-layers / middleware-chain / 用户 Effect。资源回收类留在账本，
// 由 stopAll 阶段 5（undoDeferredEffects）在 Dispose 之后兑现。
func (a *App) undoGlobalEffects(parent context.Context) {
	a.undoEffectsWhere(parent, false)
}

// undoDeferredEffects 只回放资源回收类效应（stopAll 阶段 5，AR-1 分层）。
func (a *App) undoDeferredEffects(parent context.Context) {
	a.undoEffectsWhere(parent, true)
}

// undoEffectsWhere 回放账本中 deferred 标记匹配的条目（逆序），其余保留。
// 分区回放保持各层内部相对顺序不变（与旧单层全量回放的同层顺序一致）。
func (a *App) undoEffectsWhere(parent context.Context, deferred bool) {
	a.effectsMu.Lock()
	var run []effectEntry
	kept := a.effects[:0]
	for _, e := range a.effects {
		if e.deferred == deferred {
			run = append(run, e)
		} else {
			kept = append(kept, e)
		}
	}
	a.effects = kept
	a.effectsMu.Unlock()

	for i := len(run) - 1; i >= 0; i-- {
		e := run[i]
		if e.undo == nil {
			continue
		}
		if err := a.runHook(parent, a.effectTimeout, "effect:"+e.name, e.undo); err != nil {
			log.Error(parent, "appkit effect undo failed", "effect", e.name, "error", err)
		}
	}
}
