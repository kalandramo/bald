// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kalandramo/bald/registry"
)

// fakeClock 是演练 TTL/限流逻辑的可控时钟。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1000, 0)} }

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

// mockDiscovery 记录 GetService 的调用次数。
type mockDiscovery struct {
	calls   atomic.Int32
	handler func(ctx context.Context, name string) ([]*registry.ServiceInstance, error)
}

func (m *mockDiscovery) GetService(ctx context.Context, name string) ([]*registry.ServiceInstance, error) {
	m.calls.Add(1)
	return m.handler(ctx, name)
}

func (m *mockDiscovery) Watch(ctx context.Context, name string) (registry.Watcher, error) {
	return nil, errors.New("not implemented")
}

func sampleInstances() []*registry.ServiceInstance {
	return []*registry.ServiceInstance{{ID: "n1", Name: "svc", Endpoints: []string{"grpc://127.0.0.1:9090"}}}
}

func TestCacheDeduplicatesSequential(t *testing.T) {
	fc := newFakeClock()

	m := &mockDiscovery{handler: func(context.Context, string) ([]*registry.ServiceInstance, error) {
		return sampleInstances(), nil
	}}
	c := New(m, WithNow(fc.Now), WithTTL(time.Minute), WithNodeTTL(30*time.Second))

	for i := 0; i < 3; i++ {
		if _, err := c.GetService(context.Background(), "svc"); err != nil {
			t.Fatalf("GetService() error = %v", err)
		}
	}
	if got := m.calls.Load(); got != 1 {
		t.Fatalf("underlying calls = %d, want 1", got)
	}
}

func TestCacheDeduplicatesConcurrent(t *testing.T) {
	fc := newFakeClock()

	m := &mockDiscovery{handler: func(context.Context, string) ([]*registry.ServiceInstance, error) {
		time.Sleep(5 * time.Millisecond)
		return sampleInstances(), nil
	}}
	c := New(m, WithNow(fc.Now))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.GetService(context.Background(), "svc"); err != nil {
				t.Errorf("GetService() error = %v", err)
			}
		}()
	}
	wg.Wait()
	if got := m.calls.Load(); got != 1 {
		t.Fatalf("underlying calls = %d, want 1", got)
	}
}

func TestCacheExpiresAfterTTL(t *testing.T) {
	fc := newFakeClock()

	m := &mockDiscovery{handler: func(context.Context, string) ([]*registry.ServiceInstance, error) {
		return sampleInstances(), nil
	}}
	c := New(m, WithNow(fc.Now), WithTTL(time.Second), WithNodeTTL(time.Second), WithMinRetryInterval(0))

	if _, err := c.GetService(context.Background(), "svc"); err != nil {
		t.Fatal(err)
	}
	fc.Advance(2 * time.Second)
	if _, err := c.GetService(context.Background(), "svc"); err != nil {
		t.Fatal(err)
	}
	if got := m.calls.Load(); got != 2 {
		t.Fatalf("underlying calls = %d, want 2", got)
	}
}

func TestCacheDegradesOnError(t *testing.T) {
	fc := newFakeClock()

	var fail atomic.Bool
	m := &mockDiscovery{handler: func(context.Context, string) ([]*registry.ServiceInstance, error) {
		if fail.Load() {
			return nil, errors.New("registry down")
		}
		return sampleInstances(), nil
	}}
	c := New(m, WithNow(fc.Now), WithTTL(time.Second), WithNodeTTL(time.Second), WithMinRetryInterval(0))

	if _, err := c.GetService(context.Background(), "svc"); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	fc.Advance(2 * time.Second)

	insts, err := c.GetService(context.Background(), "svc")
	if err != nil {
		t.Fatalf("expected stale fallback, got error %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("len(instances) = %d, want 1", len(insts))
	}
}

func TestCacheThrottlesRefresh(t *testing.T) {
	fc := newFakeClock()

	m := &mockDiscovery{handler: func(context.Context, string) ([]*registry.ServiceInstance, error) {
		return sampleInstances(), nil
	}}
	c := New(m, WithNow(fc.Now), WithTTL(time.Second), WithNodeTTL(time.Second), WithMinRetryInterval(5*time.Second))

	if _, err := c.GetService(context.Background(), "svc"); err != nil {
		t.Fatal(err)
	}
	// 缓存已过期但在重试间隔内：给旧数据，不刷新。
	fc.Advance(2 * time.Second)
	if _, err := c.GetService(context.Background(), "svc"); err != nil {
		t.Fatal(err)
	}
	if got := m.calls.Load(); got != 1 {
		t.Fatalf("underlying calls = %d, want 1 (throttled)", got)
	}
}

// TestCacheSnapshotIsDeepCopy 钉住文档承诺：改返回的实例不影响缓存快照
// （cloneInstances 的深拷贝不变量）。
func TestCacheSnapshotIsDeepCopy(t *testing.T) {
	fc := newFakeClock()

	m := &mockDiscovery{handler: func(context.Context, string) ([]*registry.ServiceInstance, error) {
		return sampleInstances(), nil
	}}
	c := New(m, WithNow(fc.Now)).(*cache)

	first, err := c.GetService(context.Background(), "svc")
	if err != nil {
		t.Fatal(err)
	}
	first[0].Endpoints[0] = "mutated"
	first[0].Metadata = map[string]string{"k": "v"}

	second, err := c.GetService(context.Background(), "svc")
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Endpoints[0] != "grpc://127.0.0.1:9090" || second[0].Metadata != nil {
		t.Fatalf("cached snapshot was mutated through returned reference: %+v", second[0])
	}
}

// TestCache_PruneToEmptyFallsThroughToRefresh 复现 AR-6：默认 TTL(1m)/NodeTTL(30s)
// 组合下，第 30–60s 服务级快照仍新鲜但全部节点过期——此前直接返回空列表
// （下次调用 nttls 已空又返回全量，空/满抖动）。修复后剪空即视为服务级失效
// 回源重拉。
func TestCache_PruneToEmptyFallsThroughToRefresh(t *testing.T) {
	fc := newFakeClock()
	m := &mockDiscovery{handler: func(context.Context, string) ([]*registry.ServiceInstance, error) {
		return sampleInstances(), nil
	}}
	c := New(m, WithNow(fc.Now), WithTTL(time.Minute), WithNodeTTL(30*time.Second))

	ctx := context.Background()
	if _, err := c.GetService(ctx, "svc"); err != nil {
		t.Fatalf("warm: %v", err)
	}
	if n := m.calls.Load(); n != 1 {
		t.Fatalf("warm calls = %d, want 1", n)
	}

	// 35s：服务级快照仍新鲜（TTL 1m），节点级全部过期（NodeTTL 30s）。
	fc.Advance(35 * time.Second)
	insts, err := c.GetService(ctx, "svc")
	if err != nil {
		t.Fatalf("get after 35s: %v", err)
	}
	if len(insts) == 0 {
		t.Fatal("剪空不得返回空列表（AR-6：回源重拉）")
	}
	if n := m.calls.Load(); n != 2 {
		t.Fatalf("calls = %d, want 2（剪空必须回源）", n)
	}
}

// TestCache_ThrottledPruneToEmptyReturnsStale 复现 AR-6 的限流分支：限流窗口内
// 被剪空时返回剪枝前全量快照（stale-while-error），不返回空。
func TestCache_ThrottledPruneToEmptyReturnsStale(t *testing.T) {
	fc := newFakeClock()
	m := &mockDiscovery{handler: func(context.Context, string) ([]*registry.ServiceInstance, error) {
		return sampleInstances(), nil
	}}
	// MinRetryInterval 大于推进量（35s）：第二次查询落在限流窗口内。
	c := New(m, WithNow(fc.Now), WithTTL(time.Minute), WithNodeTTL(30*time.Second), WithMinRetryInterval(time.Minute))

	ctx := context.Background()
	if _, err := c.GetService(ctx, "svc"); err != nil {
		t.Fatalf("warm: %v", err)
	}

	fc.Advance(35 * time.Second)
	insts, err := c.GetService(ctx, "svc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(insts) == 0 {
		t.Fatal("限流窗口内被剪空必须退回全量快照（stale 语义），不得返回空")
	}
	if n := m.calls.Load(); n != 1 {
		t.Fatalf("calls = %d, want 1（限流窗口内不回源）", n)
	}
}

// TestGetService_CallerDeadlineBoundsWait 复现 AR-17：等待 singleflight 结果
// 此前不看调用方 ctx——deadline=100ms 的调用方实测 1s 后才拿到结果。
// 修复后到点返回 ctx.Err()，拉取继续为其他等待者供数。
func TestGetService_CallerDeadlineBoundsWait(t *testing.T) {
	release := make(chan struct{})
	m := &mockDiscovery{handler: func(ctx context.Context, name string) ([]*registry.ServiceInstance, error) {
		<-release // 阻塞拉取直到测试放行
		return sampleInstances(), nil
	}}
	c := New(m)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.GetService(ctx, "svc")
	elapsed := time.Since(start)

	close(release) // 放行后台拉取，避免 goroutine 泄漏

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded（AR-17：等待受调用方 deadline 约束）", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("等待无界：%v 才返回（AR-17）", elapsed)
	}
}
