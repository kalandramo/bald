// Package hystrix implements a Netflix Hystrix-style circuit breaker.
//
// The Hystrix model uses a fixed-length sliding window with the following
// state machine:
//
//   - Closed: requests flow normally. The breaker tracks success/failure counts
//     over the sliding window. If the error rate exceeds errorThreshold (default
//     50%) and the request volume exceeds requestVolumeThreshold (default 20),
//     the breaker trips to Open.
//
//   - Open: all requests are rejected immediately with ErrCircuitOpen.
//     After sleepWindow (default 5s) the breaker transitions to HalfOpen.
//
//   - HalfOpen: a single trial request is allowed. If it succeeds, the breaker
//     returns to Closed. If it fails, the breaker returns to Open and the
//     sleep timer restarts.
//
// Example:
//
//	cb := hystrix.New(
//	    hystrix.WithErrorThreshold(0.5),
//	    hystrix.WithSleepWindow(5*time.Second),
//	)
//	defer cb.Close()
//
//	err := cb.Execute(ctx, func() error {
//	    return downstreamCall()
//	})
package hystrix

import (
	"context"
	"sync"
	"time"

	"github.com/kalandramo/bald/circuitbreaker"
)

var _ circuitbreaker.CircuitBreaker = (*Breaker)(nil)

const (
	defaultErrorThreshold         = 0.50 // 50% error rate
	defaultRequestVolumeThreshold = 20   // minimum requests before evaluation
	defaultSleepWindow            = 5 * time.Second
	defaultWindow                 = 10 * time.Second
	defaultBucketCount            = 10
)

// Option configures the Hystrix breaker.
type Option func(*config)

type config struct {
	errorThreshold         float64       // error rate that trips the breaker (0-1)
	requestVolumeThreshold int           // minimum requests in window before tripping
	sleepWindow            time.Duration // Open → HalfOpen transition delay
	window                 time.Duration // sliding-window length
	bucketCount            int           // number of buckets in the window
}

// WithErrorThreshold sets the error rate (0-1) that trips the breaker. Default 0.50.
func WithErrorThreshold(rate float64) Option {
	return func(c *config) {
		if rate > 0 && rate <= 1 {
			c.errorThreshold = rate
		}
	}
}

// WithRequestVolumeThreshold sets the minimum number of requests that must
// occur in the window before the breaker can trip. Default 20.
func WithRequestVolumeThreshold(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.requestVolumeThreshold = n
		}
	}
}

// WithSleepWindow sets how long the breaker stays Open before transitioning
// to HalfOpen. Default 5s.
func WithSleepWindow(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.sleepWindow = d
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

// WithBucketCount sets the number of buckets in the window. Default 10.
func WithBucketCount(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.bucketCount = n
		}
	}
}

// New creates a Hystrix-style circuit breaker.
func New(opts ...Option) *Breaker {
	cfg := &config{
		errorThreshold:         defaultErrorThreshold,
		requestVolumeThreshold: defaultRequestVolumeThreshold,
		sleepWindow:            defaultSleepWindow,
		window:                 defaultWindow,
		bucketCount:            defaultBucketCount,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	bucketDuration := cfg.window / time.Duration(cfg.bucketCount)
	return &Breaker{
		cfg:            cfg,
		bucketDuration: bucketDuration,
		buckets:        make([]hystrixBucket, cfg.bucketCount),
		lastRotate:     time.Now(),
		state:          circuitbreaker.StateClosed,
	}
}

// Breaker is a Hystrix-style circuit breaker.
type Breaker struct {
	mu             sync.Mutex
	cfg            *config
	bucketDuration time.Duration
	buckets        []hystrixBucket
	lastRotate     time.Time
	state          circuitbreaker.State
	openedAt       time.Time
	halfOpenIn     bool // true if a half-open trial is in progress
	closed         bool
}

type hystrixBucket struct {
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

	now := time.Now()

	switch b.state {
	case circuitbreaker.StateOpen:
		if now.Sub(b.openedAt) >= b.cfg.sleepWindow {
			b.state = circuitbreaker.StateHalfOpen
			b.halfOpenIn = true
		} else {
			return circuitbreaker.ErrCircuitOpen
		}
		fallthrough

	case circuitbreaker.StateHalfOpen:
		if b.halfOpenIn {
			// A trial request is already in flight — reject additional ones.
			return circuitbreaker.ErrCircuitOpen
		}
		b.halfOpenIn = true
	}

	// StateClosed — allow
	return nil
}

// MarkSuccess implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) MarkSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	b.rotateLocked(now)

	if b.state == circuitbreaker.StateHalfOpen {
		// Trial succeeded — recover
		b.state = circuitbreaker.StateClosed
		b.halfOpenIn = false
		b.resetBucketsLocked()
		return
	}

	idx := b.bucketIndexLocked(now)
	b.buckets[idx].requests++
	b.evaluateLocked()
}

// MarkFailure implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) MarkFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	b.rotateLocked(now)

	if b.state == circuitbreaker.StateHalfOpen {
		// Trial failed — re-open
		b.state = circuitbreaker.StateOpen
		b.openedAt = now
		b.halfOpenIn = false
		return
	}

	idx := b.bucketIndexLocked(now)
	b.buckets[idx].requests++
	b.buckets[idx].errors++
	b.evaluateLocked()
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
func (b *Breaker) State() circuitbreaker.State {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Lazy transition: check if Open should move to HalfOpen.
	if b.state == circuitbreaker.StateOpen &&
		time.Since(b.openedAt) >= b.cfg.sleepWindow {
		b.state = circuitbreaker.StateHalfOpen
	}

	return b.state
}

// Close implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

// evaluateLocked checks whether the breaker should trip based on the current
// window statistics. Must be called with the lock held.
func (b *Breaker) evaluateLocked() {
	if b.state != circuitbreaker.StateClosed {
		return
	}

	var totalReqs, totalErrs int64
	for _, bk := range b.buckets {
		totalReqs += bk.requests
		totalErrs += bk.errors
	}

	if totalReqs < int64(b.cfg.requestVolumeThreshold) {
		return
	}

	if totalReqs == 0 {
		return
	}

	errorRate := float64(totalErrs) / float64(totalReqs)
	if errorRate >= b.cfg.errorThreshold {
		b.state = circuitbreaker.StateOpen
		b.openedAt = time.Now()
	}
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
			b.buckets[i] = hystrixBucket{}
		}
	} else {
		for i := 0; i < steps; i++ {
			idx := (b.bucketIndexLocked(now) - steps + i + 1 + n) % n
			b.buckets[idx] = hystrixBucket{}
		}
	}
	b.lastRotate = now
}

func (b *Breaker) bucketIndexLocked(now time.Time) int {
	return int(now.UnixNano()/int64(b.bucketDuration)) % len(b.buckets)
}

func (b *Breaker) resetBucketsLocked() {
	for i := range b.buckets {
		b.buckets[i] = hystrixBucket{}
	}
}
