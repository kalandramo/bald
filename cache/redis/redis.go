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
	"strings"
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

	// AR-20：cluster 模式下 MGET 按「首个 key 的 slot」整条路由，跨 slot 直接
	// 报 CROSSSLOT——设计文档承诺按 slot 拆分但实现没拆（J8）。对齐方式：
	// 按前置判断走单机 MGET；cluster（分片）形态按 hash tag 分组并发取回合并。
	// 单机/主从形态行为不变。
	out := make([][]byte, len(keys))
	if c.isCluster() {
		results, err := c.mgetCluster(ctx, prefixed)
		if err != nil {
			return nil, err
		}
		copy(out, results)
	} else {
		results, err := c.client.MGet(ctx, prefixed...).Result()
		if err != nil {
			return nil, err
		}
		for i, r := range results {
			out[i] = redisValueToBytes(r)
		}
	}

	hasMissing := false
	for _, v := range out {
		if v == nil {
			hasMissing = true
			break
		}
	}
	if hasMissing {
		return out, cache.ErrNotFound
	}
	return out, nil
}

// isCluster 报告底层 client 是否为 cluster 形态（ClusterClient / 带
// ClusterOptions 的 UniversalClient）。
func (c *Cache) isCluster() bool {
	// NewClusterClient 返回 *ClusterClient（实现 UniversalClient）；主从/单机
	// 形态的 NewClient 返回 *Client。对 *ClusterClient 的断言即可区分两种形态
	// ——cluster 下 MGET 按 hash tag 分组（AR-20），单机保持原 MGET。
	_, ok := c.client.(*goredis.ClusterClient)
	return ok
}

// mgetCluster 把 key 按 hash tag 分组、每组一条 MGET 并发取回后按原序合并。
// 同 hash tag 的 key 天然同 slot（Redis Cluster 规范），组内 MGET 不跨 slot；
// 无 hash tag 的 key 各自成组（每 key 一条 MGET——cluster 下这是无 tag 场景
// 唯一不跨 slot 的安全形态）。
func (c *Cache) mgetCluster(ctx context.Context, prefixed []string) ([][]byte, error) {
	groups := make(map[string][]int)
	for i, k := range prefixed {
		groups[clusterKeyTag(k)] = append(groups[clusterKeyTag(k)], i)
	}

	type groupResult struct {
		idxs []int
		vals []interface{}
		err  error
	}
	ch := make(chan groupResult, len(groups))
	for _, idxs := range groups {
		go func(idxs []int) {
			keys := make([]string, len(idxs))
			for j, i := range idxs {
				keys[j] = prefixed[i]
			}
			vals, err := c.client.MGet(ctx, keys...).Result()
			ch <- groupResult{idxs: idxs, vals: vals, err: err}
		}(idxs)
	}

	out := make([][]byte, len(prefixed))
	for range groups {
		g := <-ch
		if g.err != nil {
			return nil, g.err
		}
		for j, i := range g.idxs {
			out[i] = redisValueToBytes(g.vals[j])
		}
	}
	return out, nil
}

// clusterKeyTag 提取 key 的 hash tag（{...} 内的部分，Redis Cluster 规范）；
// 无 tag 时返回整个 key（每个 key 独立 slot）。
func clusterKeyTag(key string) string {
	if a := strings.Index(key, "{"); a >= 0 {
		if b := strings.Index(key[a+1:], "}"); b >= 0 {
			return key[a+1 : a+1+b]
		}
	}
	return key
}

func redisValueToBytes(r interface{}) []byte {
	if r == nil {
		return nil
	}
	switch v := r.(type) {
	case string:
		return []byte(v)
	case []byte:
		return v
	default:
		return nil
	}
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
