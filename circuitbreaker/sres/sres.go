// Package sres implements a circuit breaker based on the Google SRE approach
// described in "Site Reliability Engineering" (Chapter 22).
//
// Unlike traditional threshold-based breakers, the SRE breaker uses a
// probabilistic acceptance model:
//
//	accept = max(0, (requests - K * errors) / (requests + 1))
//
// As the error ratio increases, the acceptance probability drops smoothly
// towards zero. There is no hard "open" / "closed" transition — instead the
// breaker probabilistically rejects requests, providing graceful degradation
// without abrupt service cutoff.
//
// The parameter K controls sensitivity:
//   - K = 1: rejects requests when error rate > 100% (never trips)
//   - K = 2: starts rejecting when error rate > 50%
//   - K = 0.5: starts rejecting when error rate > 200% (lenient)
//
// Example:
//
//	cb := sres.New(sres.WithK(2), sres.WithWindow(10*time.Second))
//	defer cb.Close()
//
//	err := cb.Execute(ctx, func() error {
//	    return downstreamCall()
//	})
package sres

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/kalandramo/bald/circuitbreaker"
)

var _ circuitbreaker.CircuitBreaker = (*Breaker)(nil)

const (
	defaultK           = 2.0
	defaultWindow      = 10 * time.Second
	defaultBucketCount = 40
)

// Option configures the SRE breaker.
type Option func(*config)

type config struct {
	k           float64       // sensitivity multiplier
	window      time.Duration // sliding-window length
	bucketCount int           // number of buckets
}

// WithK sets the sensitivity multiplier K. Default 2.0.
// Higher K is more lenient; lower K is more aggressive.
func WithK(k float64) Option {
	return func(c *config) {
		if k > 0 {
			c.k = k
		}
	}
}

// WithWindow sets the sliding-window length. Default 10s.
func WithWindow(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.window = d
		}
	}
}

// WithBucketCount sets the number of buckets in the window. Default 40.
func WithBucketCount(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.bucketCount = n
		}
	}
}

// New creates an SRE-based circuit breaker.
func New(opts ...Option) *Breaker {
	cfg := &config{
		k:           defaultK,
		window:      defaultWindow,
		bucketCount: defaultBucketCount,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	bucketDuration := cfg.window / time.Duration(cfg.bucketCount)
	return &Breaker{
		cfg:            cfg,
		bucketDuration: bucketDuration,
		buckets:        make([]sreBucket, cfg.bucketCount),
		lastRotate:     time.Now(),
	}
}

// Breaker is a Google-SRE-style circuit breaker.
type Breaker struct {
	mu             sync.Mutex
	cfg            *config
	bucketDuration time.Duration
	buckets        []sreBucket
	lastRotate     time.Time
	closed         bool
}

type sreBucket struct {
	requests int64
	errors   int64
}

// Allow implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return circuitbreaker.ErrCircuitOpen
	}

	b.rotateLocked(time.Now())

	stats := b.statsLocked()

	// SRE acceptance formula: accept = max(0, (requests - K*errors) / (requests + 1))
	var accept float64
	if stats.requests > 0 {
		accept = float64(stats.requests-b.cfg.k*float64(stats.errors)) / (float64(stats.requests) + 1)
	} else {
		accept = 1 // no data yet — allow
	}

	if accept < 0 {
		accept = 0
	}

	if accept >= 1 {
		// Fully allow
		return nil
	}

	// Probabilistic acceptance
	if rand.Float64() < accept {
		return nil
	}

	return circuitbreaker.ErrCircuitOpen
}

// MarkSuccess implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) MarkSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	b.rotateLocked(now)
	idx := b.bucketIndexLocked(now)
	b.buckets[idx].requests++
}

// MarkFailure implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) MarkFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	b.rotateLocked(now)
	idx := b.bucketIndexLocked(now)
	b.buckets[idx].requests++
	b.buckets[idx].errors++
}

// Execute implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) Execute(ctx context.Context, fn func() error) error {
	if err := b.Allow(); err != nil {
		return err
	}

	err := fn()
	if err != nil {
		b.MarkFailure()
		return err
	}

	b.MarkSuccess()
	return nil
}

// State implements [circuitbreaker.CircuitBreaker].
// The SRE breaker is always conceptually "closed" — it degrades gracefully
// rather than fully opening. We report Open when acceptance drops to 0.
func (b *Breaker) State() circuitbreaker.State {
	b.mu.Lock()
	defer b.mu.Unlock()

	stats := b.statsLocked()
	if stats.requests == 0 {
		return circuitbreaker.StateClosed
	}

	accept := (float64(stats.requests) - b.cfg.k*float64(stats.errors)) / (float64(stats.requests) + 1)
	if accept <= 0 {
		return circuitbreaker.StateOpen
	}
	if accept < 1 {
		return circuitbreaker.StateHalfOpen
	}
	return circuitbreaker.StateClosed
}

// Close implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

// windowStats aggregates the current window's request/error counts.
type windowStats struct {
	requests float64
	errors   float64
}

func (b *Breaker) statsLocked() windowStats {
	var s windowStats
	for _, bk := range b.buckets {
		s.requests += float64(bk.requests)
		s.errors += float64(bk.errors)
	}
	return s
}

func (b *Breaker) rotateLocked(now time.Time) {
	elapsed := now.Sub(b.lastRotate)
	steps := int(elapsed / b.bucketDuration)
	if steps <= 0 {
		return
	}
	n := len(b.buckets)
	if steps >= n {
		for i := range b.buckets {
			b.buckets[i] = sreBucket{}
		}
	} else {
		for i := 0; i < steps; i++ {
			idx := (b.bucketIndexLocked(now) - steps + i + 1 + n) % n
			b.buckets[idx] = sreBucket{}
		}
	}
	b.lastRotate = now
}

func (b *Breaker) bucketIndexLocked(now time.Time) int {
	return int(now.UnixNano()/int64(b.bucketDuration)) % len(b.buckets)
}
