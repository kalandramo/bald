// effect.go 实现 T1 效应账本（见 docs/devel/zh-CN/架构优化路线.md）。
//
// 背景（对照 Cordis 论文的时间可组合性）：bald 的全局注册点
// （store.RegisterTenant / audit.SetAuditor / log.SetLogger / appkit.RegisterStoreProvider）
// 此前都是不可逆效应——对共享环境的写入没有逆操作记录，覆盖即丢弃旧值。
// 进程级优雅停机（P0 三阶段）已经落地，但组件级（进程内）的「卸载零残骸」
// 完全缺失，导致：e2e 测试全局污染、重复注册语义靠约定（InitBridges 幂等）。
//
// 效应账本把这些约定变成结构保证：任何全局写入配套登记一条逆操作（undo），
// 停机（或启动失败回滚）时逆序回放，系统恢复到装配前的干净状态：
//
//	old := audit.GetAuditor()
//	audit.SetAuditor(storeAuditor)
//	app := appkit.New(
//	    appkit.Effect("audit-store", func(ctx context.Context) error {
//	        audit.SetAuditor(old) // 恢复旧值
//	        return nil
//	    }),
//	    ...
//	)
//
// e2e 测试隔离用法（免跑完整 Run）：
//
//	app := appkit.New(appkit.Effect(...))
//	t.Cleanup(func() { app.UndoEffects(context.Background()) })
package appkit

import (
	"context"
	"time"

	"github.com/kalandramo/bald/pkg/log"
)

// effectEntry 是账本中的一条可逆效应：name 用于日志定位，undo 是逆操作。
type effectEntry struct {
	name string
	undo func(ctx context.Context) error
}

// Effect 登记一条可逆效应。undo 在停机（stopAll 阶段 0，先于 BeforeStop 钩子）
// 或启动失败回滚时**逆序**执行（后注册的先撤销，与依赖建立顺序对称）。
//
// 契约（由 effect_test.go 固化）：
//   - undo 每条独立超时（EffectTimeout，默认 defaultHookTimeout）；
//   - undo panic 被隔离，不拖垮其余撤销与整机停机；
//   - 回放幂等：执行后账本清空，重复调用无副作用。
func Effect(name string, undo func(ctx context.Context) error) Option {
	return func(a *AppKit) {
		a.effects = append(a.effects, effectEntry{name: name, undo: undo})
	}
}

// EffectTimeout 设置效应撤销阶段的独立超时（每条 undo 各享此超时）。
func EffectTimeout(d time.Duration) Option {
	return func(a *AppKit) { a.effectTimeout = d }
}

// UndoEffects 立即逆序回放全部已登记效应，并清空账本（幂等）。
// 正常路径无需手动调用（Run 停机时会自动回放）；主要供 e2e 测试隔离使用：
//
//	t.Cleanup(func() { app.UndoEffects(context.Background()) })
//
// 单条 undo 超时或 panic 仅记日志，不中断其余回放（旁路语义，与审计一致）。
func (a *AppKit) UndoEffects(parent context.Context) {
	a.effectsMu.Lock()
	entries := a.effects
	a.effects = nil
	a.effectsMu.Unlock()

	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.undo == nil {
			continue
		}
		if err := a.runHook(parent, a.effectTimeout, "effect:"+e.name, e.undo); err != nil {
			log.GetLogger().Error(parent, "appkit effect undo failed", "effect", e.name, "error", err)
		}
	}
}
