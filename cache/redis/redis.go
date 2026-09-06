// Package redis provides a [cache.Cache] implementation backed by Redis
// using github.com/redis/go-redis/v9.
//
// The adapter translates the generic cache operations into Redis
// GET / SET / DEL / EXISTS commands. Values are stored as raw byte
// strings, so callers are responsible for any serialization.
//
// Example:
//
//	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
//	c := rediscache.New(rdb, rediscache.WithKeyPrefix("myapp:"))
//	defer c.Close()
//
//	_ = c.Set(ctx, "user:1", data, 10*time.Minute)
//	val, err := c.Get(ctx, "user:1")
package redis

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/kalandramo/bald/cache"
)

var _ cache.Cache = (*Cache)(nil)

// New creates a Redis-backed cache wrapping the given redis.Client.
// The caller is responsible for configuring and managing the underlying
// client (connection pooling, TLS, etc.). Close() does NOT close the
// Redis client — the caller manages its lifecycle.
func New(client goredis.UniversalClient, opts ...Option) *Cache {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}
	return &Cache{client: client, cfg: cfg}
}

// Cache is a Redis-backed implementation of [cache.Cache].
type Cache struct {
	client goredis.UniversalClient
	cfg    *config
}

// Get implements [cache.Cache].
func (c *Cache) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := c.client.Get(ctx, c.prefix(key)).Bytes()
	if err != nil {
		if err == goredis.Nil {
			return nil, cache.ErrNotFound
		}
		return nil, err
	}
	return val, nil
}

// Set implements [cache.Cache].
func (c *Cache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return c.client.Set(ctx, c.prefix(key), value, ttl).Err()
}

// SetNX implements [cache.Cache].
// Uses Redis's native atomic SET NX command (SET key value NX EX ttl).
// This is the primitive for distributed locking.
func (c *Cache) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	ok, err := c.client.SetNX(ctx, c.prefix(key), value, ttl).Result()
	if err != nil {
		return false, err
	}
	return ok, nil
}

// Delete implements [cache.Cache].
func (c *Cache) Delete(ctx context.Context, key string) error {
	return c.client.Del(ctx, c.prefix(key)).Err()
}

// Has implements [cache.Cache].
func (c *Cache) Has(ctx context.Context, key string) (bool, error) {
	n, err := c.client.Exists(ctx, c.prefix(key)).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetMulti implements [cache.Cache].
// Uses Redis's native MGET command — a single network round-trip regardless
// of how many keys are requested. Keys that do not exist yield nil entries.
func (c *Cache) GetMulti(ctx context.Context, keys []string) ([][]byte, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	prefixed := make([]string, len(keys))
	for i, k := range keys {
		prefixed[i] = c.prefix(k)
	}

	results, err := c.client.MGet(ctx, prefixed...).Result()
	if err != nil {
		return nil, err
	}

	out := make([][]byte, len(keys))
	hasMissing := false
	for i, r := range results {
		if r == nil {
			out[i] = nil
			hasMissing = true
			continue
		}
		// MGET returns values as string or []byte depending on the value type
		switch v := r.(type) {
		case string:
			out[i] = []byte(v)
		case []byte:
			out[i] = v
		default:
			out[i] = nil
			hasMissing = true
		}
	}

	if hasMissing {
		return out, cache.ErrNotFound
	}
	return out, nil
}

// SetMulti implements [cache.Cache].
// Uses Redis Pipeline to batch all SET commands into a single network
// round-trip. Each entry can have its own TTL.
func (c *Cache) SetMulti(ctx context.Context, items []cache.Item) error {
	if len(items) == 0 {
		return nil
	}

	pipe := c.client.Pipeline()
	for _, item := range items {
		pipe.Set(ctx, c.prefix(item.Key), item.Value, item.TTL)
	}

	cmders, err := pipe.Exec(ctx)
	if err != nil {
		// Even if one command fails, others may have succeeded.
		// Return the first error encountered.
		for _, cmd := range cmders {
			if cmd.Err() != nil {
				return cmd.Err()
			}
		}
		return err
	}
	return nil
}

// Close implements [cache.Cache].
// It does NOT close the underlying Redis client — the caller manages its lifecycle.
func (c *Cache) Close() error {
	return nil
}

func (c *Cache) prefix(key string) string {
	if c.cfg.keyPrefix == "" {
		return key
	}
	return c.cfg.keyPrefix + key
}
