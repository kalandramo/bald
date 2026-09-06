// Package circuitbreaker defines the circuit-breaker abstraction for the
// go-wind framework.
//
// It provides a minimal, algorithm-agnostic interface for circuit breaking.
// Concrete implementations (SRE, Hystrix, Vegas, Sentinel, etc.) implement
// this interface so that business code depends only on the contract.
package circuitbreaker

import (
	"context"
	"errors"
)

// State represents the internal state of a circuit breaker.
type State int

const (
	// StateClosed means the circuit is healthy and all requests are allowed.
	// This is the default operating state.
	StateClosed State = iota

	// StateOpen means the circuit has tripped and all requests are rejected
	// immediately with ErrCircuitOpen.
	StateOpen

	// StateHalfOpen means the circuit is testing whether the downstream
	// service has recovered. A limited number of trial requests are allowed.
	StateHalfOpen
)

// String returns a human-readable representation of the state.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// ErrCircuitOpen indicates that the request was rejected because the circuit
// breaker is in the open state.
var ErrCircuitOpen = errors.New("circuitbreaker: circuit is open")

// CircuitBreaker is the core circuit-breaking contract.
//
// Implementations must be safe for concurrent use.
type CircuitBreaker interface {
	// Allow checks whether a new request is permitted.
	//
	// If the circuit is open, it returns ErrCircuitOpen and the caller
	// should NOT call MarkSuccess or MarkFailure.
	//
	// If the circuit allows the request, the caller MUST call exactly one
	// of MarkSuccess or MarkFailure when the request completes.
	Allow() error

	// MarkSuccess records a successful request outcome.
	// It must be called exactly once after a successful Allow().
	MarkSuccess()

	// MarkFailure records a failed request outcome.
	// It must be called exactly once after a successful Allow().
	MarkFailure()

	// Execute is a convenience method that wraps Allow + fn + MarkResult.
	// If the circuit rejects the request it returns ErrCircuitOpen without
	// calling fn.
	Execute(ctx context.Context, fn func() error) error

	// State returns the current circuit-breaker state.
	State() State

	// Close releases any resources held by the circuit breaker.
	Close() error
}
