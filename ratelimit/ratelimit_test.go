package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestErrLimited(t *testing.T) {
	if !errors.Is(ErrLimited, ErrLimited) {
		t.Error("ErrLimited should match itself via errors.Is")
	}
	if ErrLimited.Error() == "" {
		t.Error("ErrLimited should have a non-empty message")
	}
}

// dummyLimiter is a minimal Limiter for interface verification.
type dummyLimiter struct{}

func (dummyLimiter) Allow() (bool, error)       { return true, nil }
func (dummyLimiter) Wait(context.Context) error { return nil }
func (dummyLimiter) Close() error               { return nil }

var _ Limiter = dummyLimiter{}

func TestLimiterInterface(t *testing.T) {
	var l Limiter = dummyLimiter{}

	ok, err := l.Allow()
	if err != nil || !ok {
		t.Errorf("Allow() = (%v, %v), want (true, nil)", ok, err)
	}

	if err := l.Wait(context.Background()); err != nil {
		t.Errorf("Wait() = %v, want nil", err)
	}

	if err := l.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

// inflightLimiter is a minimal InflightLimiter for interface verification.
type inflightLimiter struct{ dummyLimiter }

func (inflightLimiter) Done(time.Duration) {}

var _ InflightLimiter = inflightLimiter{}

func TestInflightLimiterInterface(t *testing.T) {
	var l Limiter = inflightLimiter{}

	il, ok := l.(InflightLimiter)
	if !ok {
		t.Fatal("inflightLimiter should satisfy InflightLimiter")
	}

	if ok, err := il.Allow(); err != nil || !ok {
		t.Errorf("Allow() = (%v, %v), want (true, nil)", ok, err)
	}
	il.Done(0) // must be safe to call with a zero rtt

	if err := il.Wait(context.Background()); err != nil {
		t.Errorf("Wait() = %v, want nil", err)
	}
	if err := il.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

func TestPlainLimiterIsNotInflightLimiter(t *testing.T) {
	// dummyLimiter has no Done method, so it must not satisfy the richer
	// interface — this is what keeps the type assertion meaningful.
	var l Limiter = dummyLimiter{}
	if _, ok := l.(InflightLimiter); ok {
		t.Error("dummyLimiter must not satisfy InflightLimiter (it has no Done)")
	}
}
