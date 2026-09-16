package loadable

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kalandramo/bald/cache"
)

// ---------------------------------------------------------------------------
// Functional in-test backend (no implementation dependency, per the appkit
// test discipline). Supports failure injection to exercise best-effort
// backfill and call counting for singleflight verification.
// ---------------------------------------------------------------------------

type memEntry struct {
	value []byte
	ttl   time.Duration
}

type memCache struct {
	mu         sync.RWMutex
	data       map[string]memEntry
	failSet    atomic.Bool
	setCalls   atomic.Int64
	closeCalls atomic.Int64
}

var _ cache.Cache = (*memCache)(nil)

func newMemCache() *memCache {
	return &memCache{data: make(map[string]memEntry)}
}

func (m *memCache) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.data[key]
	if !ok {
		return nil, cache.ErrNotFound
	}
	return e.value, nil
}

func (m *memCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if m.failSet.Load() {
		return errors.New("memcache: injected set failure")
	}
	m.setCalls.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = memEntry{value: value, ttl: ttl}
	return nil
}

func (m *memCache) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[key]; ok {
		return false, nil
	}
	m.data[key] = memEntry{value: value, ttl: ttl}
	return true, nil
}

func (m *memCache) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memCache) Has(_ context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *memCache) GetMulti(_ context.Context, keys []string) ([][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	values := make([][]byte, len(keys))
	missing := false
	for i, key := range keys {
		if e, ok := m.data[key]; ok {
			values[i] = e.value
		} else {
			missing = true
		}
	}
	if missing {
		return values, cache.ErrNotFound
	}
	return values, nil
}

func (m *memCache) SetMulti(ctx context.Context, items []cache.Item) error {
	for _, it := range items {
		if err := m.Set(ctx, it.Key, it.Value, it.TTL); err != nil {
			return err
		}
	}
	return nil
}

func (m *memCache) Close() error {
	m.closeCalls.Add(1)
	return nil
}

// ---------------------------------------------------------------------------
// Get: hit / miss / backfill
// ---------------------------------------------------------------------------

func TestGet_HitDoesNotLoad(t *testing.T) {
	backend := newMemCache()
	var loads atomic.Int64
	c := New(backend, func(context.Context, string) ([]byte, error) {
		loads.Add(1)
		return []byte("loaded"), nil
	})
	defer c.Close()

	if err := c.Set(context.Background(), "k", []byte("cached"), time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	val, err := c.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if string(val) != "cached" {
		t.Errorf("Get() = %q, want %q", val, "cached")
	}
	if n := loads.Load(); n != 0 {
		t.Errorf("loader called %d times on hit, want 0", n)
	}
}

func TestGet_MissLoadsAndBackfills(t *testing.T) {
	backend := newMemCache()
	var loads atomic.Int64
	c := New(backend, func(_ context.Context, key string) ([]byte, error) {
		loads.Add(1)
		return []byte("value:" + key), nil
	})
	defer c.Close()
	ctx := context.Background()

	val, err := c.Get(ctx, "user:1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if string(val) != "value:user:1" {
		t.Errorf("Get() = %q, want %q", val, "value:user:1")
	}
	if n := loads.Load(); n != 1 {
		t.Errorf("loader called %d times, want 1", n)
	}

	// Backfilled: a second Get must be served by the backend without loading.
	val, err = c.Get(ctx, "user:1")
	if err != nil {
		t.Fatalf("second Get() error = %v", err)
	}
	if string(val) != "value:user:1" {
		t.Errorf("second Get() = %q, want %q", val, "value:user:1")
	}
	if n := loads.Load(); n != 1 {
		t.Errorf("loader called %d times after backfill, want 1", n)
	}
}

// The core singleflight guarantee: N concurrent misses on one key collapse
// into exactly one loader invocation.
func TestGet_ConcurrentMissesMergedIntoSingleLoad(t *testing.T) {
	backend := newMemCache()
	var loads atomic.Int64
	c := New(backend, func(context.Context, string) ([]byte, error) {
		loads.Add(1)
		// Widen the flight window so concurrent misses join the same flight.
		time.Sleep(20 * time.Millisecond)
		return []byte("merged"), nil
	})
	defer c.Close()

	const n = 50
	var wg sync.WaitGroup
	vals := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			val, err := c.Get(context.Background(), "hot-key")
			vals[i], errs[i] = string(val), err
		}(i)
	}
	wg.Wait()

	if n := loads.Load(); n != 1 {
		t.Errorf("loader called %d times under %d concurrent misses, want 1", n, n)
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d Get() error = %v", i, errs[i])
		}
		if vals[i] != "merged" {
			t.Errorf("goroutine %d Get() = %q, want %q", i, vals[i], "merged")
		}
	}
}

// Distinct keys each get their own flight: total loads == distinct keys.
func TestGet_ConcurrentDistinctKeysLoadOnceEach(t *testing.T) {
	backend := newMemCache()
	var loads atomic.Int64
	c := New(backend, func(context.Context, string) ([]byte, error) {
		loads.Add(1)
		time.Sleep(10 * time.Millisecond)
		return []byte("v"), nil
	})
	defer c.Close()

	const keys, per = 4, 25
	var wg sync.WaitGroup
	for k := 0; k < keys; k++ {
		key := string(rune('a' + k))
		for i := 0; i < per; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = c.Get(context.Background(), key)
			}()
		}
	}
	wg.Wait()

	if n := loads.Load(); n != keys {
		t.Errorf("loader called %d times for %d distinct keys, want %d", n, keys, keys)
	}
}

// ---------------------------------------------------------------------------
// Get: error semantics
// ---------------------------------------------------------------------------

func TestGet_LoadErrorPropagatesWithoutBackfill(t *testing.T) {
	backend := newMemCache()
	boom := errors.New("source down")
	var loads atomic.Int64
	c := New(backend, func(context.Context, string) ([]byte, error) {
		loads.Add(1)
		return nil, boom
	})
	defer c.Close()
	ctx := context.Background()

	_, err := c.Get(ctx, "k")
	if !errors.Is(err, boom) {
		t.Fatalf("Get() error = %v, want %v", err, boom)
	}

	// No backfill on failure: the backend must not hold the key.
	if ok, _ := backend.Has(ctx, "k"); ok {
		t.Error("key was backfilled despite loader failure")
	}

	// Next request retries the loader naturally.
	_, _ = c.Get(ctx, "k")
	if n := loads.Load(); n != 2 {
		t.Errorf("loader called %d times after retry, want 2", n)
	}
}

func TestGet_LoadErrNotFoundPreserved(t *testing.T) {
	backend := newMemCache()
	c := New(backend, func(context.Context, string) ([]byte, error) {
		return nil, cache.ErrNotFound
	})
	defer c.Close()

	_, err := c.Get(context.Background(), "missing")
	if !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("Get() error = %v, want cache.ErrNotFound", err)
	}
}

func TestGet_BackendErrorPropagates(t *testing.T) {
	// A non-ErrNotFound backend error must surface directly without
	// reaching the loader.
	failing := &errCache{err: errors.New("backend down")}
	c := New(failing, func(context.Context, string) ([]byte, error) {
		t.Fatal("loader must not be called on backend error")
		return nil, nil
	})
	defer c.Close()

	if _, err := c.Get(context.Background(), "k"); err == nil || errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("Get() error = %v, want non-ErrNotFound backend error", err)
	}
}

// errCache fails every operation with a fixed error.
type errCache struct{ err error }

func (e *errCache) Get(context.Context, string) ([]byte, error)              { return nil, e.err }
func (e *errCache) Set(context.Context, string, []byte, time.Duration) error { return e.err }
func (e *errCache) SetNX(context.Context, string, []byte, time.Duration) (bool, error) {
	return false, e.err
}
func (e *errCache) Delete(context.Context, string) error      { return e.err }
func (e *errCache) Has(context.Context, string) (bool, error) { return false, e.err }
func (e *errCache) GetMulti(context.Context, []string) ([][]byte, error) {
	return nil, e.err
}
func (e *errCache) SetMulti(context.Context, []cache.Item) error { return e.err }
func (e *errCache) Close() error                                 { return nil }

var _ cache.Cache = (*errCache)(nil)

// Backfill is best-effort: a failing backend.Set must not fail the read.
func TestGet_BackfillFailureStillReturnsValue(t *testing.T) {
	backend := newMemCache()
	backend.failSet.Store(true)
	c := New(backend, func(context.Context, string) ([]byte, error) {
		return []byte("fresh"), nil
	})
	defer c.Close()

	val, err := c.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("Get() error = %v, want nil (best-effort backfill)", err)
	}
	if string(val) != "fresh" {
		t.Errorf("Get() = %q, want %q", val, "fresh")
	}
}

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

func TestWithTTL_BackfillUsesConfiguredTTL(t *testing.T) {
	backend := newMemCache()
	c := New(backend, func(context.Context, string) ([]byte, error) {
		return []byte("v"), nil
	}, WithTTL(7*time.Minute))
	defer c.Close()
	ctx := context.Background()

	if _, err := c.Get(ctx, "k"); err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	backend.mu.RLock()
	e, ok := backend.data["k"]
	backend.mu.RUnlock()
	if !ok {
		t.Fatal("key not backfilled")
	}
	if e.ttl != 7*time.Minute {
		t.Errorf("backfill TTL = %v, want 7m", e.ttl)
	}
}

func TestDefault_ZeroTTLBackfill(t *testing.T) {
	backend := newMemCache()
	c := New(backend, func(context.Context, string) ([]byte, error) {
		return []byte("v"), nil
	})
	defer c.Close()
	ctx := context.Background()

	if _, err := c.Get(ctx, "k"); err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	backend.mu.RLock()
	e, ok := backend.data["k"]
	backend.mu.RUnlock()
	if !ok {
		t.Fatal("key not backfilled")
	}
	if e.ttl != 0 {
		t.Errorf("backfill TTL = %v, want 0 (never expires)", e.ttl)
	}
}

// ---------------------------------------------------------------------------
// GetMulti
// ---------------------------------------------------------------------------

func TestGetMulti_MixedHitMissAndLoad(t *testing.T) {
	backend := newMemCache()
	var loads atomic.Int64
	c := New(backend, func(_ context.Context, key string) ([]byte, error) {
		loads.Add(1)
		if key == "gone" {
			return nil, cache.ErrNotFound
		}
		return []byte("loaded:" + key), nil
	})
	defer c.Close()
	ctx := context.Background()

	// Pre-seed one key directly on the backend (hit path).
	if err := backend.Set(ctx, "seeded", []byte("seeded-value"), 0); err != nil {
		t.Fatalf("seed Set() error = %v", err)
	}

	values, err := c.GetMulti(ctx, []string{"seeded", "fresh", "gone"})
	if !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("GetMulti() error = %v, want cache.ErrNotFound", err)
	}
	if len(values) != 3 {
		t.Fatalf("GetMulti() returned %d values, want 3", len(values))
	}
	if string(values[0]) != "seeded-value" {
		t.Errorf("values[0] = %q, want %q", values[0], "seeded-value")
	}
	if string(values[1]) != "loaded:fresh" {
		t.Errorf("values[1] = %q, want %q", values[1], "loaded:fresh")
	}
	if values[2] != nil {
		t.Errorf("values[2] = %q, want nil (missing)", values[2])
	}
	// "fresh" and "gone" both miss and load ("gone" resolves to
	// ErrNotFound from the loader); "seeded" hits and does not load.
	if n := loads.Load(); n != 2 {
		t.Errorf("loader called %d times, want 2", n)
	}
}

func TestGetMulti_AllPresentNoError(t *testing.T) {
	backend := newMemCache()
	c := New(backend, func(_ context.Context, key string) ([]byte, error) {
		return []byte("v:" + key), nil
	})
	defer c.Close()

	values, err := c.GetMulti(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("GetMulti() error = %v, want nil", err)
	}
	if string(values[0]) != "v:a" || string(values[1]) != "v:b" {
		t.Errorf("GetMulti() = %q, want [v:a v:b]", values)
	}
}

// ---------------------------------------------------------------------------
// Pass-through family
// ---------------------------------------------------------------------------

func TestPassThrough_OperationsReachBackend(t *testing.T) {
	backend := newMemCache()
	c := New(backend, func(context.Context, string) ([]byte, error) {
		return nil, errors.New("unexpected load")
	})
	defer c.Close()
	ctx := context.Background()

	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if ok, err := c.Has(ctx, "k"); err != nil || !ok {
		t.Errorf("Has() = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := c.SetNX(ctx, "k", []byte("v2"), time.Minute); err != nil || ok {
		t.Errorf("SetNX() = (%v, %v), want (false, nil) for existing key", ok, err)
	}
	if err := c.SetMulti(ctx, []cache.Item{{Key: "m1", Value: []byte("1")}}); err != nil {
		t.Fatalf("SetMulti() error = %v", err)
	}
	if ok, err := c.Has(ctx, "m1"); err != nil || !ok {
		t.Errorf("Has(m1) = (%v, %v), want (true, nil)", ok, err)
	}
	if err := c.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if ok, err := c.Has(ctx, "k"); err != nil || ok {
		t.Errorf("Has() after Delete = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestClose_PassesThroughToBackend(t *testing.T) {
	backend := newMemCache()
	c := New(backend, func(context.Context, string) ([]byte, error) { return nil, nil })

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if n := backend.closeCalls.Load(); n != 1 {
		t.Errorf("backend Close() called %d times, want 1", n)
	}
}
