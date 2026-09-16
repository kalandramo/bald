// Package loadable provides a read-through cache combinator on top of the
// [cache.Cache] contract: a cache miss triggers the LoadFunc to fetch the
// value from the underlying data source (typically a database) and backfill
// the cache, so business code is freed from the
// "get → miss → query source → set" boilerplate.
//
// Concurrent misses on the same key are merged into a single LoadFunc call
// via singleflight, which is the primary defense against cache stampedes
// (thundering herd) within one process. Cross-process coordination is left
// to callers composing the contract-level SetNX primitive.
//
// Example:
//
//	backend := local.New() // or redis.New(rdb)
//	c := loadable.New(backend, loadFromDB, loadable.WithTTL(5*time.Minute))
//	defer c.Close()
//
//	val, err := c.Get(ctx, "user:1") // miss → single loadFromDB → backfill
package loadable

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/kalandramo/bald/cache"
)

var _ cache.Cache = (*Cache)(nil)

// LoadFunc loads the value for a key from the underlying data source
// (typically a database query). Returning an error makes Get propagate it
// without backfilling, so the next request retries naturally. Returning
// (nil, nil) or an empty slice backfills the empty value — callers may use
// this as a business-defined negative cache.
type LoadFunc func(ctx context.Context, key string) ([]byte, error)

// Cache is a read-through combinator wrapping any [cache.Cache] backend.
// It implements [cache.Cache] itself, so it can be used wherever a plain
// cache is expected.
type Cache struct {
	backend cache.Cache
	loader  LoadFunc
	cfg     *config
	group   singleflight.Group
}

// New wraps backend with read-through semantics: a Get miss invokes loader
// (concurrent misses on the same key are merged into one call) and backfills
// the backend on success.
func New(backend cache.Cache, loader LoadFunc, opts ...Option) *Cache {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}
	return &Cache{
		backend: backend,
		loader:  loader,
		cfg:     cfg,
	}
}

// Get implements [cache.Cache]. On a backend hit it returns immediately.
// On a miss (ErrNotFound) it loads via singleflight and backfills.
//
// Backfill is best-effort: if backend.Set fails the loaded value is still
// returned — a cache write failure must not fail a read that already has
// valid data; the next request will simply miss and load again.
//
// Note on context: the first caller's context drives the merged load. If it
// is cancelled mid-load, all callers merged into that flight observe the
// cancellation error.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := c.backend.Get(ctx, key)
	if err == nil {
		return val, nil
	}
	if !errors.Is(err, cache.ErrNotFound) {
		return nil, err
	}

	v, err, _ := c.group.Do(key, func() (any, error) {
		return c.loader(ctx, key)
	})
	if err != nil {
		return nil, err
	}
	val = v.([]byte)

	// Best-effort backfill; see method doc.
	_ = c.backend.Set(ctx, key, val, c.cfg.ttl)
	return val, nil
}

// GetMulti implements [cache.Cache]: each key goes through Get, so misses
// are loaded and backfilled with the same singleflight merging. Missing
// keys yield nil entries and the overall error is ErrNotFound, matching
// the contract.
func (c *Cache) GetMulti(ctx context.Context, keys []string) ([][]byte, error) {
	values := make([][]byte, len(keys))
	missing := false
	for i, key := range keys {
		val, err := c.Get(ctx, key)
		if err != nil {
			if errors.Is(err, cache.ErrNotFound) {
				missing = true
				continue
			}
			return nil, err
		}
		values[i] = val
	}
	if missing {
		return values, cache.ErrNotFound
	}
	return values, nil
}

// Set implements [cache.Cache] by passing through to the backend.
func (c *Cache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return c.backend.Set(ctx, key, value, ttl)
}

// SetNX implements [cache.Cache] by passing through to the backend.
func (c *Cache) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	return c.backend.SetNX(ctx, key, value, ttl)
}

// Delete implements [cache.Cache] by passing through to the backend.
func (c *Cache) Delete(ctx context.Context, key string) error {
	return c.backend.Delete(ctx, key)
}

// Has implements [cache.Cache] by passing through to the backend.
func (c *Cache) Has(ctx context.Context, key string) (bool, error) {
	return c.backend.Has(ctx, key)
}

// SetMulti implements [cache.Cache] by passing through to the backend.
func (c *Cache) SetMulti(ctx context.Context, items []cache.Item) error {
	return c.backend.SetMulti(ctx, items)
}

// Close implements [cache.Cache] by closing the backend.
func (c *Cache) Close() error {
	return c.backend.Close()
}
