package loadable

import "time"

// Option configures the loadable cache combinator.
type Option func(*config)

type config struct {
	ttl time.Duration
}

// WithTTL sets the default TTL used when backfilling the cache after a
// successful load. Zero (the default) means backfilled entries never
// expire, matching the [cache.Cache] Set contract.
func WithTTL(ttl time.Duration) Option {
	return func(c *config) { c.ttl = ttl }
}
