package loadable

import "time"

// Option configures the loadable cache combinator.
type Option func(*config)

type config struct {
	ttl            time.Duration
	degradeOnError bool
}

// WithTTL sets the default TTL used when backfilling the cache after a
// successful load. Zero (the default) follows the backend's Set semantics
// for a zero TTL (local: configured default TTL; redis: never expires).
func WithTTL(ttl time.Duration) Option {
	return func(c *config) { c.ttl = ttl }
}

// WithDegradeOnError makes Get treat a backend failure (any error other than
// [cache.ErrNotFound]) as a cache miss: it falls back to the loader instead
// of surfacing the error, so a cache outage does not escalate into a business
// outage. The loader's own error is still propagated.
//
// Off by default: without it, a backend error other than ErrNotFound
// propagates directly and the loader is not called (cache failures stay
// visible). Enable this only when the cache is a pure optimization and the
// underlying data source can absorb the load while the cache is down.
func WithDegradeOnError() Option {
	return func(c *config) { c.degradeOnError = true }
}
