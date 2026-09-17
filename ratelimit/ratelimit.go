// Package ratelimit defines the rate-limiting abstraction for the bald
// framework.
//
// It provides a minimal, algorithm-agnostic interface for rate limiting.
// Concrete implementations (token bucket, BBR, Sentinel, etc.) implement this
// interface so that business code depends only on the contract.
//
// Limiters that track concurrency (rather than only rate) additionally
// implement [InflightLimiter], which pairs every admitted request with a Done.
package ratelimit

import (
	"context"
	"errors"
	"time"
)

// ErrLimited indicates that the request was rejected because the rate limit
// has been exceeded.
var ErrLimited = errors.New("ratelimit: rate limit exceeded")

// Limiter is the core rate-limiting contract.
//
// Implementations must be safe for concurrent use.
type Limiter interface {
	// Allow returns immediately. ok is true if the request is permitted;
	// ok is false (with ErrLimited) if the rate limit has been exceeded.
	Allow() (ok bool, err error)

	// Wait blocks until a request is permitted or ctx is cancelled.
	// It returns ErrLimited only if the limiter is permanently exhausted
	// (e.g. a zero-rate limiter); otherwise it blocks until tokens are
	// available.
	Wait(ctx context.Context) error

	// Close releases any resources held by the limiter.
	Close() error
}

// InflightLimiter is implemented by limiters that track in-flight requests.
//
// Allow admits a request and keeps it counted until Done is called. Every
// successful Allow must be paired with exactly one Done; an unpaired Allow
// permanently consumes capacity. For a BBR limiter the effect is dramatic: a
// single unpaired Allow caps the limiter at one concurrent request, so all
// later requests are rejected forever.
//
// Callers that hold a plain [Limiter] discover the capability by type
// assertion:
//
//	if il, ok := l.(ratelimit.InflightLimiter); ok {
//		start := time.Now()
//		if ok, err := il.Allow(); ok && err == nil {
//			defer func() { il.Done(time.Since(start)) }()
//		}
//	}
//
// rtt is the end-to-end latency of the completed request. Implementations that
// do not need it must ignore it; pass 0 when the latency is unknown.
type InflightLimiter interface {
	Limiter

	// Done marks the completion of a previously admitted request.
	Done(rtt time.Duration)
}
