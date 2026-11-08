// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package retry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// TestBulkheadFailsFastWhenFull 钉住核心语义：预算耗尽快速失败，不排队。
func TestBulkheadFailsFastWhenFull(t *testing.T) {
	b := NewBulkhead(1)
	ctx := context.Background()

	release := make(chan struct{})
	entered := make(chan struct{})

	// 占满唯一预算槽。
	go func() {
		_ = b.Do(ctx, func(ctx context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	if err := b.Do(ctx, func(context.Context) error { return nil }); !errors.Is(err, ErrBulkheadFull) {
		t.Fatalf("Do while full: err = %v, want ErrBulkheadFull", err)
	}

	close(release)
}

// TestBulkheadReleasesOnPanic 钉住预算释放：fn panic 也要归还预算（defer
// Return 语义），后续调用不受影响。
func TestBulkheadReleasesOnPanic(t *testing.T) {
	b := NewBulkhead(1)
	ctx := context.Background()

	func() {
		defer func() { _ = recover() }()
		_ = b.Do(ctx, func(context.Context) error { panic("boom") })
	}()

	// panic 后预算必须已归还。预算为 1 且 fail-fast（同时只容 1 个在途），故用
	// **顺序**调用验证：每次独占单槽、执行、归还。若 panic 泄漏了槽位，第一次
	// 后续调用即被拒（entered=0）；归还正确则三次全部进入。并发三连发会与
	// 「预算 1 快速失败」语义冲突——同时只有 1 个能进，那不是本不变量要钉的。
	entered := 0
	for i := 0; i < 3; i++ {
		err := b.Do(ctx, func(context.Context) error {
			entered++
			return nil
		})
		if err != nil {
			t.Fatalf("call %d after panic: err = %v, want nil (budget must be released)", i, err)
		}
	}
	if entered != 3 {
		t.Fatalf("only %d/3 calls entered after panic release", entered)
	}
}

// TestBulkheadDisabled maxConcurrent <= 0 禁用隔离（直通）。
func TestBulkheadDisabled(t *testing.T) {
	b := NewBulkhead(0)
	if !b.Disabled() {
		t.Fatal("maxConcurrent=0 should disable the bulkhead")
	}
	if err := b.Do(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("disabled bulkhead should pass through: %v", err)
	}
}

// TestBulkheadConcurrentBudget 并发压测：任意时刻在途数不超过预算。
func TestBulkheadConcurrentBudget(t *testing.T) {
	b := NewBulkhead(2)
	ctx := context.Background()

	var inFlight, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Do(ctx, func(context.Context) error {
				cur := inFlight.Add(1)
				for {
					p := peak.Load()
					if cur <= p || peak.CompareAndSwap(p, cur) {
						break
					}
				}
				defer inFlight.Add(-1)
				// 挤一下时间窗让并发可观测。
				for j := 0; j < 1000; j++ {
				}
				return nil
			})
		}()
	}
	wg.Wait()
	if p := peak.Load(); p > 2 {
		t.Fatalf("peak in-flight = %d, want <= 2", p)
	}
}
