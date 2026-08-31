// Package rediscache 提供基于 go-redis 的旁路缓存（Cache-Aside），
// 自 go-bald-admin 范例 internal/cache/redis 晋升（P11，见 docs/devel/zh-CN/架构优化路线.md）。
//
// 设计约束（对齐范例 §0 真实依赖契约）：
//   - 必须接真实 Redis；单测用 github.com/alicebob/miniredis 起真实内存 Redis，**不用** fake client。
//   - 采用旁路缓存（Cache-Aside）：读未命中→经 loader 加载→回填；写穿透由业务在
//     Create/Update/Delete 时调 Delete 失效。
//   - 无 Redis 环境（addr 为空）时退化为直连 loader（显式标注，非占位）。
//
// 缓存键务必含租户维度（用 Key 拼装），避免跨租户缓存泄漏。
package rediscache

import (
	"context"
	"fmt"
	"strings"
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
	// 探活：确认 Redis 真实可达，避免假连接。
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("redis: ping %s: %w", addr, err)
	}
	return &Cache{rdb: rdb, ttl: 5 * time.Minute}, nil
}

// Get 缓存读取：命中返缓存值；未命中经 loader 加载并回填 ttl。cache 禁用时直接 loader。
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

// Key 构造缓存键：各段以 ':' 连接。调用方须把租户维度放入键中，防止跨租户缓存泄漏：
//
//	rediscache.Key("secret", tenant, id) // -> "secret:t-default:s-1"
func Key(parts ...string) string {
	return strings.Join(parts, ":")
}

// Client 暴露底层 *redis.Client；rdb 为 nil 表示缓存禁用（无 Redis 环境），调用方应降级。
// 供审计消息总线等复用同一真实 Redis 连接（避免重复建连）。
func (c *Cache) Client() *redis.Client {
	return c.rdb
}
