package bbr

import (
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
