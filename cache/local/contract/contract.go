// Package contract 提供 local（进程内 freecache）后端的契约装配：
// bootstrapv1.Cache 的 local 段 → bald/cache/local 构造映射（CacheProvider
// 形状；段驱动 AppProvider 接入面见 cache/redis/contract 范本）。
//
// 单独成包的原因：local 包保持零契约依赖（纯 SDK 实现），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	baldcache "github.com/kalandramo/bald/cache"
	"github.com/kalandramo/bald/cache/local"
)

// Type 是契约 cache 段中 local（进程内）后端的段名。
const Type = "local"

// Provider 按契约 cache.local 段构造进程内缓存。
// 返回的 cleanup 由 appkit 停机 Effect 回放（Close 语义为清空内存）。
// 仅当 cache.local 段存在时被 CacheRegistry 单选分发调用（注册表机制保留
// 在 bootstrap 内部）；段驱动装配的接入面（AppProvider）见
// cache/redis/contract 范本。
func Provider(ctx context.Context, cfg *bootstrapv1.Cache) (any, func(), error) {
	sec := cfg.GetLocal()
	if sec == nil {
		return nil, nil, fmt.Errorf("cache: type=%q but local section is missing", Type)
	}
	opts := buildOptions(sec)
	c := local.New(opts...)
	return c, func() { _ = c.Close() }, nil
}

// buildOptions 契约段字段 → local options（纯函数，可测）。
// size 缺省 0 → local.New 走默认 256MB（WithSize 仅在正值时覆盖）。
func buildOptions(sec *bootstrapv1.Cache_Local) []local.Option {
	var opts []local.Option
	if n := int(sec.GetSize()); n > 0 {
		opts = append(opts, local.WithSize(n))
	}
	if s := sec.GetDefaultTtlSeconds(); s > 0 {
		opts = append(opts, local.WithDefaultTTL(time.Duration(s)*time.Second))
	}
	return opts
}

// 接口守卫：Provider 产出满足 Cache 契约。
var _ baldcache.Cache = (*local.Cache)(nil)

// 类型守卫：Provider 签名与 bootstrap.CacheProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Cache) (any, func(), error) = Provider
