// Package tokenbucket implements a classic token-bucket rate limiter.
//
// Tokens are added to the bucket at a fixed rate (rate per second) up to a
// maximum capacity (burst). Each request consumes one token. When the bucket
// is empty, requests are rejected (Allow) or delayed (Wait).
//
// Example:
//
//	limiter, _ := tokenbucket.New(100, 200) // 100 req/s, burst 200
//	defer limiter.Close()
//
//	if ok, _ := limiter.Allow(); !ok {
//	    // rate limited
//	}
package tokenbucket

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/kalandramo/bald/ratelimit"
)

var _ ratelimit.Limiter = (*Limiter)(nil)

// Option configures the token-bucket limiter.
type Option func(*config)

type config struct {
	rate  float64 // tokens per second
	burst float64 // bucket capacity
	clock func() time.Time
}

// WithClock injects a custom clock (useful for testing).
func WithClock(clock func() time.Time) Option {
	return func(c *config) { c.clock = clock }
}

// New creates a token-bucket limiter.
//
//   - rate:  sustained token replenishment rate (tokens/second). Must be > 0.
//   - burst: maximum bucket capacity (instantaneous burst). Must be > 0.
func New(rate, burst float64, opts ...Option) (*Limiter, error) {
	if rate <= 0 || burst <= 0 {
		return nil, ErrInvalidConfig
	}

	cfg := &config{
		rate:  rate,
		burst: burst,
		clock: time.Now,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return &Limiter{
		rate:   cfg.rate,
		burst:  cfg.burst,
		tokens: burst,
		last:   cfg.clock(),
		clock:  cfg.clock,
		notify: make(chan struct{}, 1),
	}, nil
}

// Limiter is a token-bucket rate limiter.
type Limiter struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	clock  func() time.Time
	notify chan struct{}
	closed bool
}

// Allow attempts to consume one token without blocking.
func (l *Limiter) Allow() (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return false, ratelimit.ErrLimited
	}

	now, wait := l.takeLocked(1)
	if wait > 0 {
		return false, ratelimit.ErrLimited
	}
	_ = now
	return true, nil
}

// Wait blocks until a token is available or ctx is cancelled.
func (l *Limiter) Wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return ratelimit.ErrLimited
		}

		_, wait := l.takeLocked(1)
		if wait <= 0 {
			l.mu.Unlock()
			return nil
		}

		// Not enough tokens — wait and retry.
		l.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		case <-l.notify:
		}
	}
}

// Close stops the limiter and wakes up any goroutine blocked on Wait.
func (l *Limiter) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.closed = true
	return nil
}

// takeLocked attempts to consume n tokens. It returns the virtual time at which
// the tokens become available and the duration the caller must wait.
// If wait is 0 the tokens were consumed immediately.
func (l *Limiter) takeLocked(n float64) (time.Time, time.Duration) {
	now := l.clock()

	// Replenish tokens based on elapsed time.
	elapsed := now.Sub(l.last).Seconds()
	if elapsed > 0 {
		l.tokens = math.Min(l.burst, l.tokens+elapsed*l.rate)
		l.last = now
	}

	if l.tokens >= n {
		l.tokens -= n
		return now, 0
	}

	deficit := n - l.tokens
	wait := time.Duration(deficit / l.rate * float64(time.Second))
	return now, wait
}
