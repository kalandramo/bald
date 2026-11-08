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

	// panic 后预算必须已归还——并发三连发全部进入（不全部被拒）。
	var wg sync.WaitGroup
	entered := make(chan struct{}, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Do(ctx, func(context.Context) error {
				entered <- struct{}{}
				return nil
			})
		}()
	}
	wg.Wait()
	if len(entered) != 3 {
		t.Fatalf("only %d/3 calls entered after panic release", len(entered))
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
