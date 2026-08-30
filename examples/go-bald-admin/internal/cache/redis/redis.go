// Package redis 提供基于 go-redis 的真实缓存层（M6.2 接成熟库，消除 §0「无外部依赖」偏差）。
//
// 设计约束（对齐 §0 真实依赖契约）：
//   - 必须接真实 Redis；单测用 github.com/alicebob/miniredis 起真实内存 Redis，**不用** fake client。
//   - 采用旁路缓存（Cache-Aside）：读未命中→经 loader 加载→回填；写穿透由业务在 Create/Update 时
//     调 Delete 失效（本范例仅读路径命中 Secret，写失效留 M6 后续）。
//   - 无 Redis 环境（BALD_ADMIN_REDIS_ADDR 为空）时退化为直连 loader（显式标注，非占位），
//     保证零 Redis 也能跑（与 SQLite 内存库同构的"真实但可选"简化）。
package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache 是旁路缓存封装。rdb 为 nil 表示禁用（直连 loader）。
type Cache struct {
	rdb *redis.Client
	ttl time.Duration
}

// New 用给定 Redis 地址构造缓存；addr 为空返回禁用态（nil rdb），调用方退化为直连。
func New(addr string) (*Cache, error) {
	if addr == "" {
		return &Cache{rdb: nil, ttl: 5 * time.Minute}, nil
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	// 探活：确认 Redis 真实可达，避免假连接（命中 §0 真实依赖）。
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("redis: ping %s: %w", addr, err)
	}
	return &Cache{rdb: rdb, ttl: 5 * time.Minute}, nil
}

// Get 缓存读取：命中返缓存值；未命中经 loader 加载并回填 ttl。cache 禁用时直接 loader。
// key 由调用方拼装（须含租户，避免跨租户泄漏）。
func (c *Cache) Get(ctx context.Context, key string, loader func(ctx context.Context) (string, error)) (string, error) {
	if c.rdb == nil {
		return loader(ctx)
	}
	val, err := c.rdb.Get(ctx, key).Result()
	if err == nil {
		return val, nil
	}
	if err != redis.Nil {
		return "", fmt.Errorf("redis: get %s: %w", key, err)
	}
	// 未命中：加载并回填。
	v, err := loader(ctx)
	if err != nil {
		return "", err
	}
	if err := c.rdb.Set(ctx, key, v, c.ttl).Err(); err != nil {
		return "", fmt.Errorf("redis: set %s: %w", key, err)
	}
	return v, nil
}

// Delete 失效指定 key（写穿透时使用）。cache 禁用时为 no-op。
func (c *Cache) Delete(ctx context.Context, key string) error {
	if c.rdb == nil {
		return nil
	}
	return c.rdb.Del(ctx, key).Err()
}

// SecretKey 构造 Secret 缓存键（含租户，防跨租户泄漏）。
func SecretKey(tenant, id string) string {
	return "secret:" + tenant + ":" + id
}

// Client 暴露底层 *redis.Client；rdb 为 nil 表示缓存禁用（无 Redis 环境），调用方应降级。
// 供审计消息总线等复用同一真实 Redis 连接（避免重复建连）。
func (c *Cache) Client() *redis.Client {
	return c.rdb
}
