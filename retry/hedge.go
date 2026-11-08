// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package retry

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// HedgeOption 配置 Hedger。
type HedgeOption func(*hedgeConfig)

type hedgeConfig struct {
	delay         time.Duration
	maxBackups    int
	backupErrRate float64
}

// WithBackupMaxRetries 设置主请求超过 delay 后发出的备份请求数。钳到 [1, 2]；默认 1。
func WithBackupMaxRetries(n int) HedgeOption {
	return func(c *hedgeConfig) {
		if n < 1 {
			n = 1
		}
		if n > 2 {
			n = 2
		}
		c.maxBackups = n
	}
}

// WithBackupErrorRate 启用备份请求的自适应节流：观测到的备份错误率达到 rate
// 时不再发备份（改为等待主请求），避免往已经在失败的下游继续堆负载。
// rate 必须在 (0, 1]；0 表示关闭节流。
func WithBackupErrorRate(rate float64) HedgeOption {
	return func(c *hedgeConfig) {
		if rate < 0 {
			rate = 0
		}
		if rate > 1 {
			rate = 1
		}
		c.backupErrRate = rate
	}
}

// Hedger 以对冲（hedged）请求降低尾延迟：主请求超过 delay 未完成时发出
// 有限数量的备份请求，谁先完成返回谁。代码自 onexmesh 移植；形态对齐本
// module 的命令式 Do 风格（Retrier 的姊妹件）。
//
// 由于并发执行 fn，fn 必须可安全并行（幂等且不共享可变副作用状态）——
// 适合 GET 这类幂等调用；不要包裹修改共享请求/响应对象的 fn。
type Hedger struct {
	cfg                hedgeConfig
	backupSuccessCount atomic.Int64
	backupTotalCount   atomic.Int64
}

// NewHedger 构造对冲器：delay 是主请求的超时阈值，超时后开始发备份请求。
func NewHedger(delay time.Duration, opts ...HedgeOption) *Hedger {
	cfg := hedgeConfig{delay: delay, maxBackups: 1}
	for _, o := range opts {
		o(&cfg)
	}
	return &Hedger{cfg: cfg}
}

// Do 执行一次对冲调用。
func (h *Hedger) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	// 派生 context：胜者返回后取消落选请求，避免它们继续运行（泄漏）。
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()

	primary := make(chan error, 1)
	go runHedge(primary, hctx, fn)

	timer := time.NewTimer(h.cfg.delay)
	defer timer.Stop()

	select {
	case err := <-primary:
		return err
	case <-timer.C:
	}

	// 自适应节流：备份请求大多在失败时退避。
	if h.cfg.backupErrRate > 0 {
		if total := h.backupTotalCount.Load(); total > 0 {
			errRate := float64(total-h.backupSuccessCount.Load()) / float64(total)
			if errRate >= h.cfg.backupErrRate {
				return <-primary
			}
		}
	}

	backup := make(chan error, h.cfg.maxBackups)
	for i := 0; i < h.cfg.maxBackups; i++ {
		go runHedge(backup, hctx, fn)
	}

	select {
	case err := <-primary:
		return err
	case err := <-backup:
		h.backupTotalCount.Add(1)
		if err == nil {
			h.backupSuccessCount.Add(1)
		}
		return err
	}
}

// runHedge 执行 fn 并把结果送入 out。recover 保证 handler panic 不击穿进程
// ——panic 以 error 形态透出，与中间件 panic 的常规处置一致。
func runHedge(out chan<- error, ctx context.Context, fn func(ctx context.Context) error) {
	defer func() {
		if r := recover(); r != nil {
			out <- fmt.Errorf("retry: hedge panic: %v", r)
		}
	}()
	out <- fn(ctx)
}
