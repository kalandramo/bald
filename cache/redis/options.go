package redis

// Option configures the Redis cache adapter.
type Option func(*config)

type config struct {
	keyPrefix string
}

// WithKeyPrefix sets a prefix prepended to every cache key.
// Useful for namespace isolation when sharing a Redis instance.
func WithKeyPrefix(prefix string) Option {
	return func(c *config) { c.keyPrefix = prefix }
}
