// Package contract 提供 redis 后端的契约装配：bootstrapv1.Cache 的 redis 段 →
// 自建 go-redis client + bald/cache/redis 适配 + CacheRegistry Provider。
//
// 单独成包的原因：redis 包保持零契约依赖（纯适配，client 由调用方注入），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
// client 生命周期归 Provider：自建（New，负责关闭）；复用注入场景请绕过
// contract 直接用 redis 包的 New（同 contrib 先例 NewWithClient 语义）。
//
// 三种部署模式按契约段选建（互斥，fail-fast）：
//   - cluster 段存在  → Redis Cluster（NewClusterClient，种子节点自动发现）；
//   - sentinel 段存在 → Redis Sentinel（NewFailoverClient，经哨兵发现主节点）；
//   - 均缺            → 单节点（NewClient，addr 必填）。
package contract

import (
	"context"
	"errors"
	"fmt"

	goredis "github.com/redis/go-redis/v9"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	baldcache "github.com/kalandramo/bald/cache"
	rediscache "github.com/kalandramo/bald/cache/redis"
)

// Type 是契约 cache 段中 redis 后端的段名。
const Type = "redis"

// Provider 按契约 cache.redis 段构造 Redis 缓存（自建 client，三种部署模式）。
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
	if err := validate(sec); err != nil {
		return nil, nil, err
	}
	client, err := dial(ctx, sec)
	if err != nil {
		return nil, nil, fmt.Errorf("cache: build redis client: %w", err)
	}
	var opts []rediscache.Option
	if p := sec.GetKeyPrefix(); p != "" {
		opts = append(opts, rediscache.WithKeyPrefix(p))
	}
	return rediscache.New(client, opts...), func() { _ = client.Close() }, nil
}

// validate 模式互斥与必填校验（纯函数，不触网络）：
//   - cluster 与 sentinel 至多其一；
//   - 单节点：addr 必填；
//   - cluster：addrs 必填；addr 须留空（防配置漂移）；db 须 0（Cluster 不支持 SELECT）；
//   - sentinel：master_name / addrs 必填；addr 须留空。
func validate(sec *bootstrapv1.Cache_Redis) error {
	cluster, sentinel := sec.GetCluster(), sec.GetSentinel()
	switch {
	case cluster != nil && sentinel != nil:
		return errors.New("cache: redis.cluster and redis.sentinel are mutually exclusive")
	case cluster != nil:
		if sec.GetAddr() != "" {
			return errors.New("cache: redis.addr is only valid in standalone mode (remove it when cluster section is present)")
		}
		if len(cluster.GetAddrs()) == 0 {
			return errors.New("cache: redis.cluster.addrs is required")
		}
		if sec.GetDb() != 0 {
			return errors.New("cache: redis.db is not supported in cluster mode")
		}
	case sentinel != nil:
		if sec.GetAddr() != "" {
			return errors.New("cache: redis.addr is only valid in standalone mode (remove it when sentinel section is present)")
		}
		if sentinel.GetMasterName() == "" {
			return errors.New("cache: redis.sentinel.master_name is required")
		}
		if len(sentinel.GetAddrs()) == 0 {
			return errors.New("cache: redis.sentinel.addrs is required")
		}
	default:
		if sec.GetAddr() == "" {
			return errors.New("cache: redis.addr is required")
		}
	}
	return nil
}

// dial 按模式建 client（须先经 validate）并 Ping 确认连通：
// 连接失败即返回错误（不产半成品实例），由 Provider 包 build 前缀。
func dial(ctx context.Context, sec *bootstrapv1.Cache_Redis) (goredis.UniversalClient, error) {
	cluster, sentinel := sec.GetCluster(), sec.GetSentinel()
	switch {
	case cluster != nil:
		copts := &goredis.ClusterOptions{
			Addrs:        cluster.GetAddrs(),
			Password:     sec.GetPassword(),
			MaxRedirects: int(cluster.GetMaxRedirects()), // 0 = go-redis 内置默认 3
		}
		client := goredis.NewClusterClient(copts)
		if err := client.Ping(ctx).Err(); err != nil {
			_ = client.Close()
			return nil, err
		}
		return client, nil
	case sentinel != nil:
		fopts := &goredis.FailoverOptions{
			MasterName:    sentinel.GetMasterName(),
			SentinelAddrs: sentinel.GetAddrs(),
			Password:      sec.GetPassword(),
			DB:            int(sec.GetDb()),
		}
		client := goredis.NewFailoverClient(fopts)
		if err := client.Ping(ctx).Err(); err != nil {
			_ = client.Close()
			return nil, err
		}
		return client, nil
	default:
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
			return nil, err
		}
		return client, nil
	}
}

// 接口守卫：Provider 产出满足 Cache 契约。
var _ baldcache.Cache = (*rediscache.Cache)(nil)

// 类型守卫：Provider 签名与 bootstrap.CacheProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Cache) (any, func(), error) = Provider
