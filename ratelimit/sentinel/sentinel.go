// Package sentinel provides a [ratelimit.Limiter] adapter backed by
// Alibaba Sentinel-Golang.
//
// Sentinel supports flow control (QPS / concurrency), circuit breaking, and
// system adaptive protection. This adapter focuses on the flow-control aspect,
// exposing it through the unified [ratelimit.Limiter] interface.
//
// Rules must be configured separately via [flow.LoadRules] or the Sentinel
// datasource API. This adapter only wraps the Entry/Exit lifecycle for a named
// resource.
//
// Example:
//
//	// 1. Initialize Sentinel (once at startup)
//	sentinel.InitDefault()
//
//	// 2. Configure a flow rule
//	flow.LoadRules([]*flow.Rule{{
//	    Resource:               "my-api",
//	    TokenCalculateStrategy: flow.Direct,
//	    ControlBehavior:        flow.Reject,
//	    Threshold:              100,
//	    StatIntervalInMs:       1000,
//	}})
//
//	// 3. Create a limiter for the resource
//	limiter := sentinel.New("my-api")
//	defer limiter.Close()
//
//	if ok, _ := limiter.Allow(); !ok {
//	    // rate limited
//	}
package sentinel

import (
	"context"
	"time"

	sentinelapi "github.com/alibaba/sentinel-golang/api"
	"github.com/alibaba/sentinel-golang/core/base"

	"github.com/kalandramo/bald/ratelimit"
)

var _ ratelimit.Limiter = (*Limiter)(nil)

// Option configures the Sentinel limiter.
type Option func(*config)

type config struct {
	trafficType  base.TrafficType
	entryOpts    []sentinelapi.EntryOption
	waitInterval time.Duration
}

// WithTrafficType sets the Sentinel traffic type (Inbound / Outbound).
// Default is [base.Inbound].
func WithTrafficType(tt base.TrafficType) Option {
	return func(c *config) { c.trafficType = tt }
}

// WithEntryOptions appends raw Sentinel entry options for advanced use cases.
func WithEntryOptions(opts ...sentinelapi.EntryOption) Option {
	return func(c *config) { c.entryOpts = opts }
}

// WithWaitInterval sets the polling interval used by Wait().
// Default is 10ms.
func WithWaitInterval(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.waitInterval = d
		}
	}
}

// New creates a Sentinel-backed limiter for the given resource name.
//
// The caller must separately configure Sentinel rules (via flow.LoadRules,
// circuitbreaker.LoadRules, etc.) for the same resource name.
func New(resource string, opts ...Option) *Limiter {
	cfg := &config{
		trafficType:  base.Inbound,
		waitInterval: 10 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return &Limiter{
		resource: resource,
		cfg:      cfg,
	}
}

// Limiter wraps a Sentinel resource as a [ratelimit.Limiter].
type Limiter struct {
	resource string
	cfg      *config
}

// Allow attempts to enter the Sentinel resource.
//
// The entry handle is discarded, so this is a one-shot check: QPS-based rules
// are evaluated at Entry time and need nothing else. Concurrency-based rules
// are NOT supported here — the entry never exits, so Sentinel's concurrency
// counter only grows. Use [Limiter.AllowEntry] plus [Limiter.ReleaseEntry] for
// those. For the same reason this type does not implement
// [ratelimit.InflightLimiter], whose Done carries no handle.
func (l *Limiter) Allow() (bool, error) {
	e, b := l.enter()
	if b != nil {
		return false, ratelimit.ErrLimited
	}
	_ = e
	return true, nil
}

// AllowEntry is like Allow but returns the Sentinel entry handle.
// The caller must call (*Limiter).ReleaseEntry(e) when done if concurrency
// limiting is configured.
func (l *Limiter) AllowEntry() (*base.SentinelEntry, error) {
	e, b := l.enter()
	if b != nil {
		return nil, ratelimit.ErrLimited
	}
	return e, nil
}

// ReleaseEntry exits a previously obtained Sentinel entry.
func (l *Limiter) ReleaseEntry(e *base.SentinelEntry) {
	if e != nil {
		e.Exit()
	}
}

// Wait blocks until the Sentinel resource admits the request or ctx is
// cancelled.
func (l *Limiter) Wait(ctx context.Context) error {
	for {
		e, b := l.enter()
		if b == nil {
			// Admitted — for concurrency rules, exit immediately since we
			// don't know when the caller's work will finish.
			if e != nil {
				e.Exit()
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(l.cfg.waitInterval):
		}
	}
}

// Close is a no-op; Sentinel manages its own lifecycle.
func (l *Limiter) Close() error {
	return nil
}

// enter calls sentinelapi.Entry with the configured options.
func (l *Limiter) enter() (*base.SentinelEntry, *base.BlockError) {
	opts := []sentinelapi.EntryOption{
		sentinelapi.WithTrafficType(l.cfg.trafficType),
	}
	opts = append(opts, l.cfg.entryOpts...)
	return sentinelapi.Entry(l.resource, opts...)
}
