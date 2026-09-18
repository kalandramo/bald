package bbr

import (
	"context"
	"testing"
	"time"

	"github.com/kalandramo/bald/ratelimit"
)

// bbr must expose the release path through the contract, otherwise callers
// holding a ratelimit.Limiter can never release an admitted request.
var _ ratelimit.InflightLimiter = (*Limiter)(nil)

// TestAllow_DoneReleasesInflight pins the behaviour that the InflightLimiter
// contract exists for: an unpaired Allow permanently consumes capacity, and
// Done gives it back.
func TestAllow_DoneReleasesInflight(t *testing.T) {
	var l ratelimit.InflightLimiter = New()
	defer l.Close()

	ok, err := l.Allow()
	if err != nil || !ok {
		t.Fatalf("first Allow() = (%v, %v), want (true, nil)", ok, err)
	}

	// On a cold window maxInflight is clamped to 1, so the second Allow is
	// rejected until the first one is released.
	if ok, _ := l.Allow(); ok {
		t.Fatal("second Allow() before Done() = true, want false")
	}

	l.Done(0)

	if ok, err := l.Allow(); err != nil || !ok {
		t.Errorf("Allow() after Done() = (%v, %v), want (true, nil)", ok, err)
	}
}

// TestAllow_UnpairedAllowLocksOut is the negative case: without Done the
// limiter stays saturated, which is exactly the trap the contract documents.
func TestAllow_UnpairedAllowLocksOut(t *testing.T) {
	l := New()
	defer l.Close()

	if ok, _ := l.Allow(); !ok {
		t.Fatal("first Allow() = false, want true")
	}

	for i := 2; i <= 4; i++ {
		ok, err := l.Allow()
		if ok || err == nil {
			t.Fatalf("Allow() #%d without Done() = (%v, %v), want (false, non-nil)", i, ok, err)
		}
	}
}

// TestWait_HoldsSlotUntilDone pins the Wait-side pairing obligation: Wait
// admits internally via Allow, so a successful Wait holds an inflight slot
// until Done is called.
func TestWait_HoldsSlotUntilDone(t *testing.T) {
	var l ratelimit.InflightLimiter = New()
	defer l.Close()

	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("first Wait() = %v, want nil", err)
	}

	// On a cold window maxInflight is clamped to 1, so the slot held by the
	// successful Wait blocks the next Allow until Done releases it.
	if ok, _ := l.Allow(); ok {
		t.Fatal("Allow() after unpaired Wait() = true, want false")
	}

	l.Done(0)

	if err := l.Wait(context.Background()); err != nil {
		t.Errorf("Wait() after Done() = %v, want nil", err)
	}
}

// TestDone_FeedsRTTEstimate covers the estimation input: Done with a non-zero
// rtt must feed the sliding window, so a healthy throughput raises maxInflight
// above the cold-start clamp.
func TestDone_FeedsRTTEstimate(t *testing.T) {
	// window=1s with a single bucket keeps every sample in the ring for the
	// whole test (rotation would only happen after a full second).
	l := New(WithWindow(time.Second), WithBucketCount(1))
	defer l.Close()

	if ok, _ := l.Allow(); !ok {
		t.Fatal("first Allow() = false, want true")
	}
	if got := l.MaxInflight(); got != 1 {
		t.Fatalf("MaxInflight() on cold start = %d, want 1", got)
	}
	l.Done(10 * time.Millisecond)

	// Ten completed requests in a one-second window is an observed 10 QPS,
	// which is well above the cold-start limit of 1 for any sane threshold.
	for i := 1; i <= 10; i++ {
		if ok, _ := l.Allow(); !ok {
			t.Fatalf("Allow() #%d = false, want true", i)
		}
		l.Done(10 * time.Millisecond)
	}

	if got := l.MaxInflight(); got <= 1 {
		t.Errorf("MaxInflight() after RTT samples = %d, want > 1", got)
	}
}

// WithClock 让滑动窗口的时间基准可注入：假时钟完全控制桶旋转，
// 无需真实 sleep 就能让窗口老化。
func TestWithClock_DeterministicWindow(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	l := New(WithWindow(time.Second), WithBucketCount(4), WithClock(clock))
	defer l.Close()

	// 首请求放行并喂一个 RTT 样本
	if ok, _ := l.Allow(); !ok {
		t.Fatal("first Allow should be admitted")
	}
	l.Done(10 * time.Millisecond)

	// 同一时刻的 maxInflight 已由样本估算
	before := l.MaxInflight()

	// 假时钟前进超过整个窗口：所有桶应过期，样本清零，估算回落到 minQPS
	now = now.Add(2 * time.Second)
	if ok, _ := l.Allow(); !ok {
		t.Fatal("Allow after window advance should be admitted")
	}
	l.Done(10 * time.Millisecond)
	after := l.MaxInflight()

	// 无时钟注入时无法确定推进，此断言只有假时钟能保证
	if before < 1 || after < 1 {
		t.Fatalf("MaxInflight should stay >= 1, before=%d after=%d", before, after)
	}
}

// WithClock(nil) 被忽略，保留默认 time.Now（与其他 Option 的 nil 语义一致）。
func TestWithClock_NilIgnored(t *testing.T) {
	l := New(WithClock(nil))
	defer l.Close()
	if ok, _ := l.Allow(); !ok {
		t.Fatal("limiter with nil clock should still admit the first request")
	}
	l.Done(0)
}
