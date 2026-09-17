// Package bbr implements a BBR-inspired adaptive rate limiter.
//
// Unlike a fixed-rate limiter, a BBR limiter continuously estimates the
// system's maximum sustainable throughput from observed latency and inflight
// counts, then adjusts the pass-through rate accordingly. This makes it
// suitable for protecting downstream services whose capacity varies with load.
//
// The algorithm maintains a sliding window of recent request statistics and
// computes maxQPS = windowSize / minRTT, capping the inflight limit at
// maxQPS * cpuThreshold.
//
// Example:
//
//	limiter := bbr.New(bbr.WithCPUThreshold(0.8), bbr.WithWindow(5*time.Second))
//	defer limiter.Close()
//
//	if ok, _ := limiter.Allow(); !ok {
//	    // rate limited — system is at capacity
//	}
package bbr

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/kalandramo/bald/ratelimit"
)

var (
	_ ratelimit.Limiter         = (*Limiter)(nil)
	_ ratelimit.InflightLimiter = (*Limiter)(nil)
)

const (
	defaultCPUThreshold = 0.80
	defaultWindow       = 10 * time.Second
	defaultBucketCount  = 40
	defaultMinQPS       = 1.0
)

// Option configures the BBR limiter.
type Option func(*config)

type config struct {
	cpuThreshold float64       // CPU/load threshold (0-1) above which requests are throttled
	window       time.Duration // sliding-window length
	bucketCount  int           // number of buckets in the window
	minQPS       float64       // minimum allowed QPS even under heavy load
}

// WithCPUThreshold sets the CPU/load threshold (0-1). Default 0.80.
func WithCPUThreshold(threshold float64) Option {
	return func(c *config) {
		if threshold > 0 && threshold < 1 {
			c.cpuThreshold = threshold
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

// WithBucketCount sets the number of buckets within the window. Default 40.
func WithBucketCount(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.bucketCount = n
		}
	}
}

// WithMinQPS sets the minimum allowed QPS. Default 1.0.
func WithMinQPS(qps float64) Option {
	return func(c *config) {
		if qps > 0 {
			c.minQPS = qps
		}
	}
}

// New creates a BBR adaptive limiter.
func New(opts ...Option) *Limiter {
	cfg := &config{
		cpuThreshold: defaultCPUThreshold,
		window:       defaultWindow,
		bucketCount:  defaultBucketCount,
		minQPS:       defaultMinQPS,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	bucketDuration := cfg.window / time.Duration(cfg.bucketCount)
	l := &Limiter{
		cfg:            cfg,
		bucketDuration: bucketDuration,
		buckets:        make([]bucket, cfg.bucketCount),
		lastBucketTime: time.Now(),
	}

	return l
}

// Limiter is a BBR adaptive rate limiter.
type Limiter struct {
	mu             sync.Mutex
	cfg            *config
	bucketDuration time.Duration
	buckets        []bucket
	lastBucketTime time.Time
	inflight       int64
	lastDropTime   time.Time
	maxInflight    int64
	closed         bool
}

type bucket struct {
	startTime int64
	count     int64
	totalRTT  int64 // nanoseconds
}

// Allow checks whether a new request should be admitted.
// If admitted, the caller must call Done() when the request finishes.
func (l *Limiter) Allow() (bool, error) {
	l.mu.Lock()

	if l.closed {
		l.mu.Unlock()
		return false, ratelimit.ErrLimited
	}

	now := time.Now()
	l.rotateLocked(now)

	// Estimate max QPS and max inflight.
	maxQPS := l.estimateMaxQPSLocked()
	maxInflight := int64(maxQPS * l.cfg.cpuThreshold)
	if maxInflight < 1 {
		maxInflight = 1
	}
	l.maxInflight = maxInflight

	if l.inflight >= maxInflight {
		l.lastDropTime = now
		l.mu.Unlock()
		return false, ratelimit.ErrLimited
	}

	l.inflight++
	l.mu.Unlock()
	return true, nil
}

// Wait blocks until a request can be admitted or ctx is cancelled.
// On success the admitted slot is held until Done is called — Wait admits
// internally via Allow, so the InflightLimiter pairing obligation applies
// to a successful Wait as well.
func (l *Limiter) Wait(ctx context.Context) error {
	for {
		ok, err := l.Allow()
		if err == nil && ok {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Done marks the completion of a previously admitted request.
// rtt is the end-to-end latency of the request (for RTT estimation).
//
// Done is what satisfies [ratelimit.InflightLimiter]. Every successful Allow
// must be paired with exactly one Done — through the [ratelimit.Limiter]
// interface the pairing is the caller's responsibility, and an unpaired Allow
// caps this limiter at one concurrent request.
func (l *Limiter) Done(rtt time.Duration) {
	l.mu.Lock()
	l.inflight--

	now := time.Now()
	l.rotateLocked(now)

	idx := l.currentBucketIndexLocked(now)
	l.buckets[idx].count++
	l.buckets[idx].totalRTT += int64(rtt)
	l.mu.Unlock()
}

// Close releases resources held by the limiter.
func (l *Limiter) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	return nil
}

// MaxInflight returns the most recently computed inflight limit.
func (l *Limiter) MaxInflight() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.maxInflight
}

// rotateLocked advances the bucket ring to the current time, clearing expired buckets.
func (l *Limiter) rotateLocked(now time.Time) {
	elapsed := now.Sub(l.lastBucketTime)
	steps := int(elapsed / l.bucketDuration)
	if steps <= 0 {
		return
	}
	n := len(l.buckets)
	if steps >= n {
		for i := range l.buckets {
			l.buckets[i] = bucket{}
		}
	} else {
		for i := 0; i < steps; i++ {
			idx := l.currentBucketIndexLocked(now) - steps + i + 1
			if idx < 0 {
				idx += n
			}
			idx %= n
			l.buckets[idx] = bucket{}
		}
	}
	l.lastBucketTime = now
}

// currentBucketIndexLocked returns the ring index for the given time.
func (l *Limiter) currentBucketIndexLocked(now time.Time) int {
	return int(now.UnixNano()/int64(l.bucketDuration)) % len(l.buckets)
}

// estimateMaxQPSLocked computes the estimated maximum QPS from the sliding window.
// maxQPS = windowSize / minRTT
func (l *Limiter) estimateMaxQPSLocked() float64 {
	var totalCount int64
	var minRTT time.Duration = math.MaxInt64
	zero := true

	for _, b := range l.buckets {
		if b.count > 0 {
			zero = false
			totalCount += b.count
			avgRTT := time.Duration(b.totalRTT / b.count)
			if avgRTT < minRTT {
				minRTT = avgRTT
			}
		}
	}

	if zero || minRTT <= 0 {
		return l.cfg.minQPS
	}

	qps := float64(totalCount) / l.cfg.window.Seconds()
	if qps < l.cfg.minQPS {
		qps = l.cfg.minQPS
	}

	// maxPass = windowSize / minRTT
	maxPass := l.cfg.window.Seconds() / minRTT.Seconds()
	if maxPass < qps {
		qps = maxPass
	}

	return math.Max(qps, l.cfg.minQPS)
}
