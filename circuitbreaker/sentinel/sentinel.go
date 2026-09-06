// Package sentinel provides a [circuitbreaker.CircuitBreaker] adapter backed
// by Alibaba Sentinel-Golang's circuit-breaker module.
//
// Sentinel supports three circuit-breaking strategies:
//   - Slow RT ratio: trips when the ratio of slow requests exceeds threshold
//   - Error ratio: trips when the error ratio exceeds threshold
//   - Error count: trips when the error count exceeds threshold
//
// Rules must be configured separately via the Sentinel circuitbreaker.LoadRules
// API or a datasource. This adapter wraps the Entry/Exit lifecycle for a named
// resource and maps Sentinel's circuit state to the
// [circuitbreaker.CircuitBreaker] interface.
//
// Prefer Execute() over Allow/MarkSuccess/MarkFailure — the Execute path
// properly records latency and errors through Sentinel's stat slots.
//
// Example:
//
//	// 1. Initialize Sentinel
//	sentinelapi.InitDefault()
//
//	// 2. Configure a circuit-breaker rule
//	cbRules := []*circuitbreaker.Rule{{
//	    Resource:         "my-api",
//	    Strategy:         circuitbreaker.ErrorRatio,
//	    Threshold:        0.5,
//	    RetryTimeoutMs:   5000,
//	    MinRequestAmount: 10,
//	    StatIntervalMs:   10000,
//	}}
//	circuitbreaker.LoadRules(cbRules)
//
//	// 3. Create a breaker for the resource
//	cb := sentinel.New("my-api")
//	defer cb.Close()
//
//	err := cb.Execute(ctx, func() error {
//	    return downstreamCall()
//	})
package sentinel

import (
	"context"
	"sync"

	sentinelapi "github.com/alibaba/sentinel-golang/api"
	"github.com/alibaba/sentinel-golang/core/base"
	scb "github.com/alibaba/sentinel-golang/core/circuitbreaker"

	windcb "github.com/kalandramo/bald/circuitbreaker"
)

var _ windcb.CircuitBreaker = (*Breaker)(nil)

// Option configures the Sentinel breaker.
type Option func(*config)

type config struct {
	trafficType base.TrafficType
	entryOpts   []sentinelapi.EntryOption
}

// WithTrafficType sets the Sentinel traffic type. Default is [base.Outbound].
func WithTrafficType(tt base.TrafficType) Option {
	return func(c *config) { c.trafficType = tt }
}

// WithEntryOptions appends raw Sentinel entry options.
func WithEntryOptions(opts ...sentinelapi.EntryOption) Option {
	return func(c *config) { c.entryOpts = opts }
}

// New creates a Sentinel-backed circuit breaker for the given resource.
//
// The caller must separately configure Sentinel circuit-breaker rules for
// the same resource name via circuitbreaker.LoadRules.
func New(resource string, opts ...Option) *Breaker {
	cfg := &config{
		trafficType: base.Outbound,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return &Breaker{
		resource: resource,
		cfg:      cfg,
	}
}

// Breaker wraps a Sentinel resource as a [windcb.CircuitBreaker].
type Breaker struct {
	mu       sync.Mutex
	resource string
	cfg      *config
	entry    *base.SentinelEntry // entry from the most recent Allow() call
}

// Allow implements [windcb.CircuitBreaker].
// It returns ErrCircuitOpen if Sentinel blocks the request (circuit open
// or any other slot rejection).
//
// The caller must call MarkSuccess or MarkFailure when the request completes
// so that the Sentinel entry is properly closed.
func (b *Breaker) Allow() error {
	e, blockErr := b.enter()
	if blockErr != nil {
		return windcb.ErrCircuitOpen
	}

	b.mu.Lock()
	b.entry = e
	b.mu.Unlock()
	return nil
}

// MarkSuccess implements [windcb.CircuitBreaker].
// It closes the Sentinel entry opened by the preceding Allow() call,
// recording the request as successful.
func (b *Breaker) MarkSuccess() {
	b.mu.Lock()
	e := b.entry
	b.entry = nil
	b.mu.Unlock()

	if e != nil {
		e.Exit()
	}
}

// MarkFailure implements [windcb.CircuitBreaker].
// It records the error on the Sentinel entry and closes it, allowing
// Sentinel's circuit-breaker slot to evaluate the failure.
func (b *Breaker) MarkFailure() {
	b.mu.Lock()
	e := b.entry
	b.entry = nil
	b.mu.Unlock()

	if e != nil {
		sentinelapi.TraceError(e, windcb.ErrCircuitOpen)
		e.Exit()
	}
}

// Execute implements [windcb.CircuitBreaker].
// It wraps the function call with a full Sentinel Entry/Exit lifecycle.
// If fn returns an error, Sentinel's circuit-breaker stat slot records it.
func (b *Breaker) Execute(ctx context.Context, fn func() error) error {
	e, blockErr := b.enter()
	if blockErr != nil {
		return windcb.ErrCircuitOpen
	}

	err := fn()

	if e != nil {
		if err != nil {
			sentinelapi.TraceError(e, err)
		}
		e.Exit()
	}

	return err
}

// State implements [windcb.CircuitBreaker].
// It maps Sentinel's circuit-breaker state to our state enum.
// Since Sentinel does not expose per-resource breaker state through a public
// API, we check GetRulesOfResource to determine if rules are configured.
// Without rules, the circuit is always Closed.
func (b *Breaker) State() windcb.State {
	rules := scb.GetRulesOfResource(b.resource)
	if len(rules) == 0 {
		return windcb.StateClosed
	}
	// Sentinel manages the actual state internally.
	// A trial Entry can tell us if the circuit is currently open.
	e, blockErr := b.enter()
	if e != nil {
		e.Exit()
		return windcb.StateClosed
	}
	if blockErr != nil && blockErr.BlockType() == base.BlockTypeCircuitBreaking {
		return windcb.StateOpen
	}
	return windcb.StateClosed
}

// Close implements [windcb.CircuitBreaker].
func (b *Breaker) Close() error {
	return nil
}

// enter calls sentinelapi.Entry with the configured options.
func (b *Breaker) enter() (*base.SentinelEntry, *base.BlockError) {
	opts := []sentinelapi.EntryOption{
		sentinelapi.WithTrafficType(b.cfg.trafficType),
	}
	opts = append(opts, b.cfg.entryOpts...)
	return sentinelapi.Entry(b.resource, opts...)
}
