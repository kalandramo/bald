package tokenbucket

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kalandramo/bald/ratelimit"
)

// ---------------------------------------------------------------------------
// New — validation
// ---------------------------------------------------------------------------

func TestNew_ValidConfig(t *testing.T) {
	l, err := New(100, 200)
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	defer l.Close()
	if l == nil {
		t.Fatal("New() returned nil limiter")
	}
}

func TestNew_ZeroRateFails(t *testing.T) {
	_, err := New(0, 100)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("New(0, 100) err = %v, want ErrInvalidConfig", err)
	}
}

func TestNew_NegativeRateFails(t *testing.T) {
	_, err := New(-1, 100)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("New(-1, 100) err = %v, want ErrInvalidConfig", err)
	}
}

func TestNew_ZeroBurstFails(t *testing.T) {
	_, err := New(100, 0)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("New(100, 0) err = %v, want ErrInvalidConfig", err)
	}
}

// ---------------------------------------------------------------------------
// Allow — burst capacity
// ---------------------------------------------------------------------------

func TestAllow_AllowsBurstCapacity(t *testing.T) {
	l, _ := New(1, 5) // 1 req/s, burst 5
	defer l.Close()

	for i := 0; i < 5; i++ {
		ok, err := l.Allow()
		if err != nil || !ok {
			t.Errorf("Allow() #%d = (%v, %v), want (true, nil)", i, ok, err)
		}
	}
}

func TestAllow_RejectsBeyondBurst(t *testing.T) {
	l, _ := New(1, 3) // 1 req/s, burst 3
	defer l.Close()

	// Consume all burst tokens
	for i := 0; i < 3; i++ {
		l.Allow()
	}

	ok, err := l.Allow()
	if ok {
		t.Error("Allow() after burst exhausted should return false")
	}
	if !errors.Is(err, ratelimit.ErrLimited) {
		t.Errorf("Allow() err = %v, want ErrLimited", err)
	}
}

// ---------------------------------------------------------------------------
// Allow — token replenishment (with mock clock)
// ---------------------------------------------------------------------------

func TestAllow_TokenReplenishment(t *testing.T) {
	now := time.Now()
	mockClock := func() time.Time { return now }

	l, _ := New(10, 2, WithClock(mockClock)) // 10 req/s, burst 2
	defer l.Close()

	// Exhaust burst
	l.Allow()
	l.Allow()

	// No tokens yet — rejected
	ok, _ := l.Allow()
	if ok {
		t.Error("expected rejection after burst exhaustion")
	}

	// Advance time by 200ms → 2 tokens replenished (10 req/s * 0.2s = 2)
	now = now.Add(200 * time.Millisecond)

	ok, _ = l.Allow()
	if !ok {
		t.Error("expected Allow after 200ms to succeed (2 tokens replenished)")
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestClose_RejectsAfterClose(t *testing.T) {
	l, _ := New(100, 100)
	l.Close()

	ok, err := l.Allow()
	if ok {
		t.Error("Allow() after Close should return false")
	}
	if !errors.Is(err, ratelimit.ErrLimited) {
		t.Errorf("Allow() err after Close = %v, want ErrLimited", err)
	}
}

func TestClose_WaitReturnsErrAfterClose(t *testing.T) {
	l, _ := New(1, 1)
	l.Allow() // exhaust burst

	go func() {
		time.Sleep(50 * time.Millisecond)
		l.Close()
	}()

	err := l.Wait(context.Background())
	if !errors.Is(err, ratelimit.ErrLimited) {
		t.Errorf("Wait() after Close = %v, want ErrLimited", err)
	}
}

// TestClose_WakesBlockedWait pins the wake-up itself. The clock is frozen and the
// refill rate is 0.0001/s, so the waiter's computed wait is ~10000s: without
// Close signalling the channel it would sit in time.After and only notice the
// closed flag hours later. (The weaker case above passes even without the
// wake-up, because New(1, 1) happens to compute a 1s wait.)
func TestClose_WakesBlockedWait(t *testing.T) {
	frozen := time.Now()
	l, _ := New(0.0001, 1, WithClock(func() time.Time { return frozen }))

	l.Allow() // exhaust the only token

	done := make(chan error, 1)
	go func() { done <- l.Wait(context.Background()) }()

	time.Sleep(20 * time.Millisecond) // let the waiter park in the select
	if err := l.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, ratelimit.ErrLimited) {
			t.Errorf("Wait() after Close = %v, want ErrLimited", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait() still blocked 2s after Close — waiter was not woken")
	}
}

// TestClose_IsIdempotent second Close must not panic (the wake-up channel is
// closed exactly once) and must not report an error.
func TestClose_IsIdempotent(t *testing.T) {
	l, _ := New(10, 1)
	if err := l.Close(); err != nil {
		t.Fatalf("first Close() = %v, want nil", err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("second Close() = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// Wait — blocks then succeeds
// ---------------------------------------------------------------------------

func TestWait_BlocksThenSucceeds(t *testing.T) {
	l, _ := New(100, 1) // 100 req/s, burst 1
	defer l.Close()

	// Exhaust burst
	l.Allow()

	// Next Allow should be rejected immediately
	ok, _ := l.Allow()
	if ok {
		t.Error("expected rejection after burst exhaustion")
	}

	// Wait should block until token replenishes (~10ms at 100 req/s)
	done := make(chan error, 1)
	go func() {
		done <- l.Wait(context.Background())
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Wait() = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Wait() timed out — expected token replenishment")
	}
}

// ---------------------------------------------------------------------------
// Wait — context cancellation
// ---------------------------------------------------------------------------

func TestWait_ContextCancelled(t *testing.T) {
	l, _ := New(0.01, 1) // extremely slow: 0.01 req/s = 1 token / 100s
	defer l.Close()

	// Exhaust burst
	l.Allow()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := l.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Wait() with short deadline = %v, want DeadlineExceeded", err)
	}
}
