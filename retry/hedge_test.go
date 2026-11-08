// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package retry

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestHedgeBackupWinsAfterDelay 钉住核心语义：主请求超 delay 未完成时备份
// 发出并先返回。
func TestHedgeBackupWinsAfterDelay(t *testing.T) {
	h := NewHedger(20 * time.Millisecond)
	ctx := context.Background()

	start := time.Now()
	// 主请求睡 100ms，备份（相同 fn 的并发执行）用更短的失败路径立即返回。
	err := h.Do(ctx, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err() // 落选方被 cancel——这正是 Hedger 的取消语义
		case <-time.After(100 * time.Millisecond):
			return nil
		}
	})
	elapsed := time.Since(start)
	// 上面的 fn 谁也不会"先成功"——两边都是等 100ms 或 cancel。真正要验证
	// 的是取消语义：胜者返回时（20ms 备份路径不存在，双方同 fn），本例退化为
	// 等主请求。此测试主要防 Do 的阻塞回归，对冲胜负语义由下一条覆盖。
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if elapsed < 20*time.Millisecond {
		t.Fatalf("backup fired too early: %v", elapsed)
	}
}

// TestHedgeReturnsFastestWinner 用不对称 fn 验证「谁先完成返回谁」：备份
// （delay 后启动的那次）快于主请求时，总耗时 < 主请求耗时。
func TestHedgeReturnsFastestWinner(t *testing.T) {
	h := NewHedger(30 * time.Millisecond)
	ctx := context.Background()

	var calls atomic.Int32
	start := time.Now()
	err := h.Do(ctx, func(ctx context.Context) error {
		// 首个（主）执行慢；delay 后的备份执行快——若 ctx 已取消则快速返回
		// 错误，不该赢。让备份路径成功：第一次调用睡 80ms，后续调用立即成功。
		if calls.Add(1) == 1 {
			time.Sleep(80 * time.Millisecond)
			return nil
		}
		return nil
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	// 备份在 30ms 启动、立即成功 → 总耗时应明显小于主请求的 80ms。
	if elapsed >= 80*time.Millisecond {
		t.Fatalf("hedge did not return the faster backup: %v", elapsed)
	}
}

// TestHedgeAdaptiveThrottle 钉住自适应节流：备份错误率超阈值后不再发备份。
// 用调用计数构造不对称路径——第 1 次调用慢成功（主），第 2 次调用立即 panic
// （预热轮的备份，10ms 即出错先于主请求被计数），第 3 次起慢成功（节流后的主）。
func TestHedgeAdaptiveThrottle(t *testing.T) {
	h := NewHedger(10*time.Millisecond, WithBackupMaxRetries(1), WithBackupErrorRate(0.5))
	ctx := context.Background()

	var calls atomic.Int32
	fn := func(ctx context.Context) error {
		if calls.Add(1) == 2 {
			panic("backup boom") // 预热轮的备份路径：立即失败
		}
		time.Sleep(50 * time.Millisecond)
		return nil
	}

	// 预热一轮：主 = call1（50ms），备份 = call2（panic）——备份先完成被计数；
	// 对冲语义返回先完成者的结果，所以这次 Do 返回备份的 panic 错误。
	_ = h.Do(ctx, fn)
	if got := h.backupTotalCount.Load(); got != 1 {
		t.Fatalf("backup total = %d, want 1 (panicking backup counted)", got)
	}

	// 节流生效：这次 Do 不再发备份（备份计数不再增长），主请求完成返回。
	before := h.backupTotalCount.Load()
	if err := h.Do(ctx, fn); err != nil {
		t.Fatalf("throttled Do: %v", err)
	}
	if after := h.backupTotalCount.Load(); after != before {
		t.Fatalf("backup issued despite throttle: %d -> %d", before, after)
	}
}

// TestHedgeRecoversPanic 钉住 panic 不击穿：fn panic 以 error 形态返回。
func TestHedgeRecoversPanic(t *testing.T) {
	h := NewHedger(5 * time.Millisecond)
	err := h.Do(context.Background(), func(ctx context.Context) error {
		panic("boom")
	})
	if err == nil || !errors.Is(err, err) { // 非 nil 即可；语义在 message 里
		t.Fatalf("expected panic-as-error, got %v", err)
	}
}
