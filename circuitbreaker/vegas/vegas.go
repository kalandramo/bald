// Package vegas implements a circuit breaker inspired by TCP Vegas congestion
// control.
//
// TCP Vegas detects congestion early by comparing the observed round-trip time
// (RTT) against a baseline RTT (the minimum observed). When the difference
// exceeds a threshold, Vegas infers queuing delay and reduces the sending rate.
//
// Applied to circuit breaking:
//
//   - BaseRTT is the minimum observed latency (healthy baseline).
//   - CurrentRTT is an exponentially-smoothed average of recent latencies.
//   - Queue delay = CurrentRTT - BaseRTT
//   - When queue delay / BaseRTT exceeds alphaThreshold, the circuit degrades.
//   - When it recovers below betaThreshold, the circuit heals.
//
// This provides early detection of downstream degradation — before hard
// failures occur — based on latency inflation alone.
//
// Example:
//
//	cb := vegas.New(
//	    vegas.WithAlpha(0.5),  // degrade when RTT inflation > 50%
//	    vegas.WithBeta(0.3),   // heal when RTT inflation < 30%
//	)
//	defer cb.Close()
//
//	err := cb.Execute(ctx, func() error {
//	    start := time.Now()
//	    err := downstreamCall()
//	    cb.RecordLatency(time.Since(start))
//	    return err
//	})
package vegas

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/kalandramo/bald/circuitbreaker"
)

var _ circuitbreaker.CircuitBreaker = (*Breaker)(nil)

const (
	defaultAlpha         = 0.5 // degrade threshold: RTT inflation > 50%
	defaultBeta          = 0.3 // heal threshold: RTT inflation < 30%
	defaultWarmupSamples = 10  // requests before RTT baseline stabilizes
	defaultMinRTT        = time.Millisecond
	defaultMaxRTT        = 30 * time.Second
)

// Option configures the Vegas breaker.
type Option func(*config)

type config struct {
	alpha         float64       // degrade when (currentRTT - baseRTT) / baseRTT > alpha
	beta          float64       // heal when inflation < beta
	warmupSamples int           // samples before the breaker starts evaluating
	minRTT        time.Duration // minimum plausible RTT
	maxRTT        time.Duration // maximum plausible RTT (outlier filter)
}

// WithAlpha sets the degradation threshold (RTT inflation ratio). Default 0.5.
func WithAlpha(a float64) Option {
	return func(c *config) {
		if a > 0 {
			c.alpha = a
		}
	}
}

// WithBeta sets the healing threshold. Default 0.3.
// Beta must be less than Alpha for hysteresis.
func WithBeta(b float64) Option {
	return func(c *config) {
		if b > 0 {
			c.beta = b
		}
	}
}

// WithWarmupSamples sets the number of samples before evaluation begins. Default 10.
func WithWarmupSamples(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.warmupSamples = n
		}
	}
}

// New creates a Vegas-inspired circuit breaker.
func New(opts ...Option) *Breaker {
	cfg := &config{
		alpha:         defaultAlpha,
		beta:          defaultBeta,
		warmupSamples: defaultWarmupSamples,
		minRTT:        defaultMinRTT,
		maxRTT:        defaultMaxRTT,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return &Breaker{
		cfg:         cfg,
		baseRTT:     0,
		currentRTT:  0,
		sampleCount: 0,
		state:       circuitbreaker.StateClosed,
	}
}

// Breaker is a Vegas-inspired latency-based circuit breaker.
type Breaker struct {
	mu            sync.Mutex
	cfg           *config
	baseRTT       time.Duration // minimum observed RTT (healthy baseline)
	currentRTT    time.Duration // exponentially smoothed RTT
	sampleCount   int
	state         circuitbreaker.State
	probeInFlight bool // true while an Open-state probe request is outstanding
	closed        bool
}

// Allow implements [circuitbreaker.CircuitBreaker].
//
// In the Open and HalfOpen states it admits exactly one probe request at a
// time. This is not an optimization — it is required for correctness: Vegas
// decides recovery from latency samples, and latency samples can only come
// from requests that actually run. Unconditionally rejecting while Open would
// sever the breaker's only input channel and lock it open forever. The probe
// is what re-measures the downstream after a degradation.
func (b *Breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return circuitbreaker.ErrCircuitOpen
	}

	if b.state == circuitbreaker.StateOpen || b.state == circuitbreaker.StateHalfOpen {
		if b.probeInFlight {
			// A probe is already outstanding — reject the rest to protect
			// the downstream while we re-measure it.
			return circuitbreaker.ErrCircuitOpen
		}
		b.probeInFlight = true
	}

	return nil
}

// MarkSuccess implements [circuitbreaker.CircuitBreaker].
// Use RecordLatency instead to provide timing data — MarkSuccess alone
// records a zero latency which is filtered out as an outlier and therefore
// does not feed the Vegas estimator. It still releases an outstanding probe,
// so the breaker never locks up; recovering from Open requires real latency
// samples (via Execute or an explicit RecordLatency).
func (b *Breaker) MarkSuccess() {
	b.RecordLatency(0)
}

// MarkFailure implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) MarkFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Release the probe slot on every completion path — an unreleased slot
	// would re-introduce the permanent lock-out this breaker used to have.
	b.probeInFlight = false

	if b.state == circuitbreaker.StateHalfOpen {
		b.state = circuitbreaker.StateOpen
		return
	}

	// Repeated failures also degrade — treat as high latency.
	b.updateRTTLocked(b.cfg.maxRTT)
	b.evaluateLocked()
}

// RecordLatency reports the latency of a completed request.
// This is the primary input for the Vegas algorithm. Call it after every
// successful request (in addition to or instead of MarkSuccess).
func (b *Breaker) RecordLatency(rtt time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Release the probe slot on every completion path (see MarkFailure).
	b.probeInFlight = false

	if b.state == circuitbreaker.StateHalfOpen {
		b.state = circuitbreaker.StateClosed
	}

	b.updateRTTLocked(rtt)
	b.evaluateLocked()
}

// Execute implements [circuitbreaker.CircuitBreaker].
// It automatically records the latency of successful requests.
func (b *Breaker) Execute(ctx context.Context, fn func() error) error {
	if err := b.Allow(); err != nil {
		return err
	}

	start := time.Now()
	err := fn()
	elapsed := time.Since(start)

	if err != nil {
		b.MarkFailure()
		return err
	}

	b.RecordLatency(elapsed)
	return nil
}

// State implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) State() circuitbreaker.State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Close implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

// updateRTTLocked updates the base and smoothed RTT values.
func (b *Breaker) updateRTTLocked(rtt time.Duration) {
	// Filter outliers
	if rtt < b.cfg.minRTT || rtt > b.cfg.maxRTT {
		return
	}

	b.sampleCount++

	// Exponential smoothing: currentRTT = 0.875 * currentRTT + 0.125 * rtt
	if b.currentRTT == 0 {
		b.currentRTT = rtt
	} else {
		b.currentRTT = time.Duration(float64(b.currentRTT)*0.875 + float64(rtt)*0.125)
	}

	// Base RTT is the minimum observed
	if b.baseRTT == 0 || rtt < b.baseRTT {
		b.baseRTT = rtt
	}
}

// evaluateLocked checks whether the RTT inflation warrants a state change.
func (b *Breaker) evaluateLocked() {
	if b.sampleCount < b.cfg.warmupSamples || b.baseRTT == 0 {
		return
	}

	inflation := float64(b.currentRTT-b.baseRTT) / float64(b.baseRTT)

	switch b.state {
	case circuitbreaker.StateClosed:
		if inflation > b.cfg.alpha {
			b.state = circuitbreaker.StateOpen
		}

	case circuitbreaker.StateOpen:
		// Check if enough time has passed — Vegas uses latency recovery
		// rather than a fixed timer. When inflation drops below beta,
		// transition through HalfOpen.
		if inflation < b.cfg.beta {
			b.state = circuitbreaker.StateHalfOpen
		}

	case circuitbreaker.StateHalfOpen:
		// Stay here until a sample confirms recovery
		if inflation < b.cfg.beta {
			b.state = circuitbreaker.StateClosed
		} else if inflation > b.cfg.alpha {
			b.state = circuitbreaker.StateOpen
		}
	}
}

// BaseRTT returns the current baseline RTT (for observability).
func (b *Breaker) BaseRTT() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.baseRTT
}

// CurrentRTT returns the smoothed current RTT (for observability).
func (b *Breaker) CurrentRTT() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentRTT
}

// Inflation returns the current RTT inflation ratio (for observability).
// Returns 0 if baseRTT is not yet established.
func (b *Breaker) Inflation() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.baseRTT == 0 {
		return 0
	}
	return math.Max(0, float64(b.currentRTT-b.baseRTT)/float64(b.baseRTT))
}
