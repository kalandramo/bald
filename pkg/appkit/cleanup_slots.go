// 清理槽位域（ADR-8 拆分自 bootstrap.go）：cleanupSlots 是阶段 B 产物的
// cleanup 账本——同键 replace（热更换芯）/ fill（首发）/ effects（停机 Effect
// 回放）；storeProduct 系列把 Provider 产物按顶层段落进资源面并落槽。
package appkit

import (
	"context"
	"sync/atomic"

	"github.com/kalandramo/bald/log"
)

func newCleanupSlots(plan *providerPlan) *cleanupSlots {
	s := &cleanupSlots{
		cleanups:  make(map[ResourceKey]*Cleanup, len(plan.teardown)),
		order:     plan.teardown,
		name:      plan.effectName,
		loggerKey: plan.loggerNode.Key,
	}
	for _, key := range plan.teardown {
		c := Cleanup(nil)
		s.cleanups[key] = &c
	}
	return s
}

// replace 以新 cleanup 替换节点槽位并同步兑现旧钩子（配置协调路径；
// 返回旧钩子的错误，调用方决定是否仅记日志）。
func (s *cleanupSlots) replace(ctx context.Context, key ResourceKey, c Cleanup) {
	slot := s.cleanups[key]
	if slot == nil {
		return
	}
	old := *slot
	*slot = c
	if old != nil {
		if err := old(ctx); err != nil {
			log.Error(ctx, "appkit node cleanup failed during reconcile", "node", string(key), "error", err)
		}
	}
}

// fill 填充节点槽位（同 Key 只 Build 一次；聚合分支保留给防御性重复填充，
// 后填者先执行）。
func (s *cleanupSlots) fill(key ResourceKey, c Cleanup) {
	slot := s.cleanups[key]
	if slot == nil || c == nil {
		return
	}
	if *slot == nil {
		*slot = c
		return
	}
	prev := *slot
	*slot = func(ctx context.Context) error {
		if err := c(ctx); err != nil {
			return err
		}
		return prev(ctx)
	}
}

// effects 生成停机 Effect：按停机序逆序注册，回放即停机序（I5）。logger
// 槽位特供 bootstrap-logger——恢复装配前全局 Logger 并兑现最新后端 cleanup
// （热更重建原子换槽的钩子在此收口）。
func (s *cleanupSlots) effects(oldLogger log.Logger, curCleanup *atomic.Pointer[func()]) []Option {
	opts := make([]Option, 0, len(s.order))
	for i := len(s.order) - 1; i >= 0; i-- {
		key := s.order[i]
		name := s.name[key]
		if key == s.loggerKey {
			opts = append(opts, effectOpt(name, func(context.Context) error {
				log.SetLogger(oldLogger)
				if c := curCleanup.Load(); c != nil {
					(*c)()
				}
				return nil
			}))
			continue
		}
		slot := s.cleanups[key]
		opts = append(opts, effectOpt(name, func(ctx context.Context) error {
			if c := *slot; c != nil {
				return c(ctx)
			}
			return nil
		}))
	}
	return opts
}

// noopCleanupFn 把 Cleanup 转成无参聚合用闭包（config 层 cleanup 聚合）。
func noopCleanupFn(c Cleanup) func() {
	if c == nil {
		return nil
	}
	return func() { _ = c(context.Background()) }
}
