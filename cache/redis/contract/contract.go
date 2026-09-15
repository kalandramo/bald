// Package contract 提供 redis 后端的契约装配：bootstrapv1.Cache 的 redis 段 →
// 自建 go-redis client + bald/cache/redis 适配 + CacheRegistry Provider。
//
// 单独成包的原因：redis 包保持零契约依赖（纯适配，client 由调用方注入），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
// client 生命周期归 Provider：自建（New，负责关闭）；复用注入场景请绕过
// contract 直接用 redis 包的 New（同 contrib 先例 NewWithClient 语义）。
package contract

import (
	"context"
	"fmt"

	goredis "github.com/redis/go-redis/v9"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	baldcache "github.com/kalandramo/bald/cache"
	rediscache "github.com/kalandramo/bald/cache/redis"
)

// Type 是契约 cache 段中 redis 后端的段名。
const Type = "redis"

// Provider 按契约 cache.redis 段构造 Redis 缓存（自建 client）。
// 返回的 cleanup 由 appkit 停机 Effect 回放（关闭底层连接池）。
// 仅当 cache.redis 段存在时被 CacheRegistry 调度到。
// 签名与 bootstrap.CacheProvider 结构化兼容，直接注册：
//
//	cr := bootstrap.NewCacheRegistry()
//	cr.MustRegister(rediscontract.Type, rediscontract.Provider)
func Provider(ctx context.Context, cfg *bootstrapv1.Cache) (any, func(), error) {
	sec := cfg.GetRedis()
	if sec == nil {
		return nil, nil, fmt.Errorf("cache: type=%q but redis section is missing", Type)
	}
	if sec.GetAddr() == "" {
		return nil, nil, fmt.Errorf("cache: redis.addr is required")
	}

	client, cacheImpl, err := build(ctx, sec)
	if err != nil {
		return nil, nil, fmt.Errorf("cache: build redis client: %w", err)
	}
	return cacheImpl, func() { _ = client.Close() }, nil
}

// build 自建 client + 适配（拆出便于测试注入 miniredis 地址）。
func build(ctx context.Context, sec *bootstrapv1.Cache_Redis) (*goredis.Client, baldcache.Cache, error) {
	ropts := &goredis.Options{
		Addr: sec.GetAddr(),
		DB:   int(sec.GetDb()),
	}
	if p := sec.GetPassword(); p != "" {
		ropts.Password = p
	}
	client := goredis.NewClient(ropts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	var opts []rediscache.Option
	if p := sec.GetKeyPrefix(); p != "" {
		opts = append(opts, rediscache.WithKeyPrefix(p))
	}
	return client, rediscache.New(client, opts...), nil
}

// 接口守卫：Provider 产出满足 Cache 契约。
var _ baldcache.Cache = (*rediscache.Cache)(nil)

// 类型守卫：Provider 签名与 bootstrap.CacheProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Cache) (any, func(), error) = Provider
