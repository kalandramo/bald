// Package ratelimit defines the rate-limiting abstraction for the go-wind
// framework.
//
// It provides a minimal, algorithm-agnostic interface for rate limiting.
// Concrete implementations (token bucket, BBR, Sentinel, etc.) implement this
// interface so that business code depends only on the contract.
package ratelimit

import (
	"context"
	"errors"
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
