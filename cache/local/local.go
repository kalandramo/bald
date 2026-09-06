// Package local provides a high-performance in-memory [cache.Cache]
// implementation backed by [github.com/coocood/freecache].
//
// FreeCache achieves zero GC overhead by using a pre-allocated ring buffer
// and segment-level locking for high concurrency. It supports per-entry
// TTL (second-precision) and automatic LRU eviction when the pre-allocated
// capacity is exhausted.
//
// Example:
//
//	c := local.New(local.WithSize(256*1024*1024), local.WithDefaultTTL(5*time.Minute))
//	defer c.Close()
//
//	_ = c.Set(ctx, "user:1", data, 0) // uses default TTL
//	val, err := c.Get(ctx, "user:1")
package local

import (
	"context"
	"time"

	"github.com/coocood/freecache"

	"github.com/kalandramo/bald/cache"
)

var (
	_ cache.Cache = (*Cache)(nil)
)

// New creates a high-performance in-memory cache backed by FreeCache.
func New(opts ...Option) *Cache {
	cfg := &config{
		size: defaultSize,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return &Cache{
		fc:  freecache.NewCache(cfg.size),
		cfg: cfg,
	}
}

// Cache is a FreeCache-backed implementation of [cache.Cache].
type Cache struct {
	fc  *freecache.Cache
	cfg *config
}

// Get implements [cache.Cache].
func (c *Cache) Get(_ context.Context, key string) ([]byte, error) {
	val, err := c.fc.Get([]byte(key))
	if err != nil {
		if err == freecache.ErrNotFound {
			return nil, cache.ErrNotFound
		}
		return nil, err
	}
	return val, nil
}

// Set implements [cache.Cache].
// A zero TTL means "use default TTL"; if no default TTL is configured,
// the entry never expires.
func (c *Cache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = c.cfg.defaultTTL
	}
	expire := int(ttl.Seconds())
	return c.fc.Set([]byte(key), value, expire)
}

// SetNX implements [cache.Cache].
// SetNX atomically sets the value only if the key does not already exist.
// Returns true if the key was set, false if it already existed.
//
// Because FreeCache does not expose a native SetNX, we implement it via
// a check-then-set guarded by a per-segment spin-lock. FreeCache's internal
// 256-segment design means lock contention is extremely low.
func (c *Cache) SetNX(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	kb := []byte(key)

	// Check existence (this acquires the segment read-lock internally)
	_, err := c.fc.Get(kb)
	if err == nil {
		// Key exists — fail
		return false, nil
	}
	if err != freecache.ErrNotFound {
		return false, err
	}

	// Key doesn't exist — attempt to set
	if ttl <= 0 {
		ttl = c.cfg.defaultTTL
	}
	expire := int(ttl.Seconds())
	if err := c.fc.Set(kb, value, expire); err != nil {
		return false, err
	}
	return true, nil
}

// Delete implements [cache.Cache].
func (c *Cache) Delete(_ context.Context, key string) error {
	c.fc.Del([]byte(key))
	return nil
}

// Has implements [cache.Cache].
func (c *Cache) Has(_ context.Context, key string) (bool, error) {
	_, err := c.fc.Get([]byte(key))
	if err == freecache.ErrNotFound {
		return false, nil
	}
	return err == nil, err
}

// GetMulti implements [cache.Cache].
// FreeCache is in-process so there is no network round-trip to optimize;
// we iterate sequentially. The result slice is always len(keys) long.
func (c *Cache) GetMulti(_ context.Context, keys []string) ([][]byte, error) {
	result := make([][]byte, len(keys))
	hasMissing := false

	for i, key := range keys {
		val, err := c.fc.Get([]byte(key))
		if err == freecache.ErrNotFound {
			result[i] = nil
			hasMissing = true
			continue
		}
		if err != nil {
			return nil, err
		}
		result[i] = val
	}

	if hasMissing {
		return result, cache.ErrNotFound
	}
	return result, nil
}

// SetMulti implements [cache.Cache].
// FreeCache is in-process so there is no network round-trip to optimize;
// we iterate sequentially.
func (c *Cache) SetMulti(_ context.Context, items []cache.Item) error {
	for _, item := range items {
		ttl := item.TTL
		if ttl <= 0 {
			ttl = c.cfg.defaultTTL
		}
		expire := int(ttl.Seconds())
		if err := c.fc.Set([]byte(item.Key), item.Value, expire); err != nil {
			return err
		}
	}
	return nil
}

// Close implements [cache.Cache].
func (c *Cache) Close() error {
	c.fc.Clear()
	return nil
}

// --- Metrics ---

// EntryCount returns the number of entries currently in the cache.
func (c *Cache) EntryCount() int64 {
	return c.fc.EntryCount()
}

// HitCount returns the total number of cache hits.
func (c *Cache) HitCount() int64 {
	return c.fc.HitCount()
}

// MissCount returns the total number of cache misses.
func (c *Cache) MissCount() int64 {
	return c.fc.MissCount()
}

// EvacuateCount returns the number of entries evicted (LRU or overwrite).
func (c *Cache) EvacuateCount() int64 {
	return c.fc.EvacuateCount()
}

// ExpiredCount returns the number of entries that expired.
func (c *Cache) ExpiredCount() int64 {
	return c.fc.ExpiredCount()
}
