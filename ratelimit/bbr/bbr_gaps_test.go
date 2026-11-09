package bbr

import (
	"context"
	"testing"
	"time"

	"github.com/kalandramo/bald/ratelimit"
)

// TestWithCPUThreshold 合法阈值生效，非法值（≤0 或 ≥1）保留缺省。
func TestWithCPUThreshold(t *testing.T) {
	l := New(WithCPUThreshold(0.5))
	defer func() { _ = l.Close() }()
	if l.cfg.cpuThreshold != 0.5 {
		t.Errorf("cpuThreshold = %v, want 0.5", l.cfg.cpuThreshold)
	}
	for _, bad := range []float64{0, 1, -0.1, 1.5} {
		l2 := New(WithCPUThreshold(bad))
		if l2.cfg.cpuThreshold != defaultCPUThreshold {
			t.Errorf("WithCPUThreshold(%v) = %v, want default %v", bad, l2.cfg.cpuThreshold, defaultCPUThreshold)
		}
		_ = l2.Close()
	}
}

// TestWithMinQPS 合法 QPS 生效，非正值保留缺省。
func TestWithMinQPS(t *testing.T) {
	l := New(WithMinQPS(5))
	defer func() { _ = l.Close() }()
	if l.cfg.minQPS != 5 {
		t.Errorf("minQPS = %v, want 5", l.cfg.minQPS)
	}
	for _, bad := range []float64{0, -1} {
		l2 := New(WithMinQPS(bad))
		if l2.cfg.minQPS != defaultMinQPS {
			t.Errorf("WithMinQPS(%v) = %v, want default %v", bad, l2.cfg.minQPS, defaultMinQPS)
		}
		_ = l2.Close()
	}
}

// TestAllow_AfterClose 关闭后 Allow 一律拒绝并返回 ErrLimited。
func TestAllow_AfterClose(t *testing.T) {
	l := New()
	_ = l.Close()
	ok, err := l.Allow()
	if ok || err != ratelimit.ErrLimited {
		t.Errorf("Allow after Close = (%v, %v), want (false, ErrLimited)", ok, err)
	}
}

// TestWait_ContextCancelled Allow 持续失败时 Wait 应响应 ctx 取消并返回 ctx.Err()。
func TestWait_ContextCancelled(t *testing.T) {
	l := New()
	_ = l.Close() // Allow 恒失败 → Wait 进入 select 循环

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 已取消，select 必然命中 ctx.Done()

	if err := l.Wait(ctx); err != context.Canceled {
		t.Errorf("Wait = %v, want context.Canceled", err)
	}
}

// TestRotateLocked_Partial 部分旋转（0 < steps < n）：仅清除过期桶，保留近期桶；
// 构造 cur=3、steps=5 使首跳 idx=-1 触发环形回绕（idx+=n）。
func TestRotateLocked_Partial(t *testing.T) {
	now := time.Unix(38, 0)
	l := New(WithWindow(40*time.Second), WithBucketCount(40), WithClock(func() time.Time { return now }))
	defer func() { _ = l.Close() }()
	// bucketDuration=1s，lastBucketTime=38。预置两个桶。
	l.buckets[39] = bucket{count: 5, totalRTT: 100}
	l.buckets[10] = bucket{count: 7, totalRTT: 200}

	now = time.Unix(43, 0) // elapsed=5s → steps=5；cur=43%40=3
	l.mu.Lock()
	l.rotateLocked(now)
	l.mu.Unlock()

	// 清除 idx 39,0,1,2,3（i=0 时 idx=3-5+1=-1 → 回绕 39）。
	if l.buckets[39].count != 0 {
		t.Errorf("expired bucket 39 not cleared: %+v", l.buckets[39])
	}
	if l.buckets[10].count != 7 {
		t.Errorf("bucket 10 should survive partial rotation: %+v", l.buckets[10])
	}
}

// TestEstimateMaxQPS_MinQPSFloor 观测 QPS 低于 minQPS 时回落到 minQPS。
func TestEstimateMaxQPS_MinQPSFloor(t *testing.T) {
	now := time.Unix(100, 0)
	l := New(WithWindow(10*time.Second), WithMinQPS(100), WithClock(func() time.Time { return now }))
	defer func() { _ = l.Close() }()
	l.Done(time.Millisecond) // count=1 → 观测 qps=0.1 < 100

	l.mu.Lock()
	qps := l.estimateMaxQPSLocked()
	l.mu.Unlock()
	if qps != 100 {
		t.Errorf("qps = %v, want minQPS floor 100", qps)
	}
}

// TestEstimateMaxQPS_MaxPassCap 观测 QPS 超过 window/minRTT 上限时封顶到 maxPass。
func TestEstimateMaxQPS_MaxPassCap(t *testing.T) {
	now := time.Unix(100, 0)
	l := New(WithWindow(time.Second), WithClock(func() time.Time { return now }))
	defer func() { _ = l.Close() }()
	// count=10、avgRTT=1s：观测 qps=10，maxPass=1s/1s=1 < 10 → 封顶 1。
	l.buckets[0] = bucket{count: 10, totalRTT: int64(10 * time.Second)}

	l.mu.Lock()
	qps := l.estimateMaxQPSLocked()
	l.mu.Unlock()
	if qps != 1 {
		t.Errorf("qps = %v, want maxPass cap 1", qps)
	}
}
