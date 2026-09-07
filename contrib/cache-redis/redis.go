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

// options 聚合 New 的可选参数（零值即原行为）。
type options struct {
	password string
	db       int
	ttl      time.Duration
}

// Option 是 New 的可选参数。
type Option func(*options)

// WithPassword 设置 Redis 密码（云端带认证实例必需）。
func WithPassword(password string) Option {
	return func(o *options) { o.password = password }
}

// WithDB 设置 Redis 逻辑库编号（默认 0）。
func WithDB(db int) Option {
	return func(o *options) { o.db = db }
}

// WithTTL 覆盖回填缓存 TTL（默认 5 分钟）。
func WithTTL(ttl time.Duration) Option {
	return func(o *options) { o.ttl = ttl }
}

// New 用给定 Redis 地址构造缓存；addr 为空返回禁用态（nil rdb），调用方退化为直连。
// 可选参数注入密码/逻辑库/TTL；不传时与旧行为完全一致（兼容既有调用方）。
func New(addr string, opts ...Option) (*Cache, error) {
	o := &options{ttl: 5 * time.Minute}
	for _, opt := range opts {
		opt(o)
	}
	if addr == "" {
		return &Cache{rdb: nil, ttl: o.ttl}, nil
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: o.password, DB: o.db})
	// 探活：确认 Redis 真实可达，避免假连接。
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("redis: ping %s: %w", addr, err)
	}
	return &Cache{rdb: rdb, ttl: o.ttl}, nil
}

// Get 缓存读取：命中返缓存值；未命中或 Redis 故障时经 loader 加载并回填 ttl。
// cache 禁用时直接 loader。
//
// T4 起降级语义：Redis 故障（非 Nil 错误，连接失败/超时等）时降级直连 loader——
// 缓存故障不放大为业务故障（T4 字典验收「Redis 停机降级直连 store」由此兑现；
// 此前故障直接报错，会把缓存层问题变成业务 5xx）。降级路径回填尽力而为：Set
// 失败静默（缓存是优化非职责），代价是停机期间每次读都先承受一次 GET 超时开销。
func (c *Cache) Get(ctx context.Context, key string, loader func(ctx context.Context) (string, error)) (string, error) {
	if c.rdb == nil {
		return loader(ctx)
	}
	if val, err := c.rdb.Get(ctx, key).Result(); err == nil {
		return val, nil
	}
	// 未命中（Nil）与故障（非 Nil）统一走 loadAndBackfill。
	return c.loadAndBackfill(ctx, key, loader)
}

// loadAndBackfill 经 loader 加载并尽力回填：Set 失败静默返回（降级容错）。
func (c *Cache) loadAndBackfill(ctx context.Context, key string, loader func(ctx context.Context) (string, error)) (string, error) {
	v, err := loader(ctx)
	if err != nil {
		return "", err
	}
	_ = c.rdb.Set(ctx, key, v, c.ttl).Err() // 回填是优化，失败不阻塞读
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
