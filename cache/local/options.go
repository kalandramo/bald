package local

import "time"

const (
	// defaultSize is the default pre-allocated cache size: 256 MB.
	defaultSize = 256 * 1024 * 1024
)

// Option configures the local cache.
type Option func(*config)

type config struct {
	size       int           // pre-allocated cache size in bytes
	defaultTTL time.Duration // default TTL when Set is called with ttl=0
}

// WithSize sets the pre-allocated cache size in bytes.
// Larger sizes reduce eviction but consume more memory.
// Default 256 MB.
func WithSize(size int) Option {
	return func(c *config) {
		if size > 0 {
			c.size = size
		}
	}
}

// WithDefaultTTL sets a default TTL applied when Set is called with a zero TTL.
// By default, entries never expire.
// Note: FreeCache TTL has second-precision; sub-second durations are rounded up.
func WithDefaultTTL(ttl time.Duration) Option {
	return func(c *config) { c.defaultTTL = ttl }
}
