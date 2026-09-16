// Package cache defines the caching abstraction for the bald framework.
//
// It provides a minimal, backend-agnostic interface for key-value caching
// with TTL support. Concrete implementations (in-memory, Redis, etc.)
// implement this interface so that business code depends only on the contract.
package cache

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound indicates that the requested key was not found in the cache.
var ErrNotFound = errors.New("cache: key not found")

// Item represents a single key-value entry for batch operations.
type Item struct {
	Key   string
	Value []byte
	TTL   time.Duration
}

// Cache is the core caching contract.
//
// All operations accept a context for cancellation and timeout control.
// Implementations must be safe for concurrent use.
type Cache interface {
	// Get retrieves the value for the given key.
	// Returns ErrNotFound if the key does not exist or has expired.
	Get(ctx context.Context, key string) ([]byte, error)

	// Set stores the value with the given key and TTL.
	// A zero TTL is backend-defined: local applies its configured default
	// TTL (never expires if unset); redis never expires.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error

	// SetNX sets the value only if the key does not already exist.
	// Returns true if the key was set (i.e. it did not exist before).
	// This is the primitive for distributed locking and cache-penetration
	// defense (mutex at cache-miss to prevent thundering-herd / DB stampede).
	SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)

	// Delete removes the value for the given key.
	// It is a no-op if the key does not exist.
	Delete(ctx context.Context, key string) error

	// Has reports whether the key exists and has not expired.
	Has(ctx context.Context, key string) (bool, error)

	// GetMulti retrieves values for multiple keys in a single round-trip.
	// For each key that is missing or expired the corresponding value is nil
	// and the error is ErrNotFound. The returned slice is always len(keys) long
	// and aligned with the input order.
	//
	// Backends with native MGET/Pipeline support (e.g. Redis) execute this as
	// one network I/O; local backends iterate internally.
	GetMulti(ctx context.Context, keys []string) ([][]byte, error)

	// SetMulti stores multiple key-value entries in a single round-trip.
	// A zero TTL on an Item follows the same backend-defined semantics as Set.
	//
	// Backends with native MSET/Pipeline support (e.g. Redis) execute this as
	// one network I/O; local backends iterate internally.
	SetMulti(ctx context.Context, items []Item) error

	// Close releases any resources held by the cache.
	Close() error
}
