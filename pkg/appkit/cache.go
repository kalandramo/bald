// cache.go 是 appkit 侧的缓存装配胶水：契约 cache 段的 Registry（构造期）
// 已迁 bootstrap（2026-09-15，见 bootstrap/cache.go），本文件保留运行期
// 装配面——WithCacheRegistry Option、buildCaches 胶水、Cache/Caches 访问器。
//
// 分工（构造期归 bootstrap，运行期归 appkit）：
//   - bootstrap.CacheRegistry：显式注册 Provider、段枚举、Build；
//   - appkit：BeforeStart（阶段 B，契约装载校验后）调 Build，实例存
//     a.caches；cleanup 挂停机 Effect——注册在 database-clients 之后，
//     逆序回放保证「缓存先关（加速层，关了不影响正确性）、数据库连接
//     最后关」。cache 段不支持热更新。
//
// 用法：
//
//	cr := bootstrap.NewCacheRegistry()
//	cr.MustRegister(localcontract.Type, localcontract.Provider)  // "local"
//	cr.MustRegister(rediscontract.Type, rediscontract.Provider)  // "redis"
//	app, err := appkit.FromBootstrap(cfg, appkit.WithCacheRegistry(cr), ...)
//	// Run 后取实例：
//	c := app.Cache(rediscontract.Type).(cache.Cache)
package appkit

import (
	"context"
	"errors"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
)

// WithCacheRegistry 注入缓存后端的契约装配注册表（显式注册 provider，
// 见 bootstrap.CacheRegistry）。契约 cache 段任一后端段存在时，阶段 B
// 按段名查表构建缓存实例；cache 段不支持热更新。
func WithCacheRegistry(r *baldbootstrap.CacheRegistry) BootstrapOption {
	return func(s *bootstrapSpec) { s.cacheRegistry = r }
}

// buildCaches 在阶段 B（契约装载校验后）按契约 cache 段构建全部已配置
// 后端的缓存实例，存入 a.caches 供业务经 Cache/Caches 取用。cache 段
// 缺失为 no-op；段存在但未接 CacheRegistry 则 fail-fast。
// 返回聚合 cleanup（可为 nil），由 FromBootstrap 挂停机 Effect。
func buildCaches(a *AppKit, cfg *bootstrapv1.BootstrapConfig, spec *bootstrapSpec) (func(), error) {
	ccfg := cfg.GetCache()
	if ccfg == nil {
		return nil, nil // 未配置任何缓存
	}
	if spec.cacheRegistry == nil {
		return nil, errors.New("appkit: bootstrap.cache present but no CacheRegistry wired (WithCacheRegistry missing)")
	}
	clients, cleanup, err := spec.cacheRegistry.Build(context.Background(), ccfg)
	if err != nil {
		return nil, err
	}
	a.caches = clients
	return cleanup, nil
}

// Cache 返回按契约段装配的后端缓存实例（阶段 B 后、Run 期可用；
// FromBootstrap 构造期尚未装载契约，返回空）。
// typ 为契约段名（见 cache/<backend>/contract.Type，如 "redis"）。
// 并发语义：caches 只在 BeforeStart（阶段 B）单次写入，Run 后只读，
// happens-before 由 Run 启动流程保证。
func (k *AppKit) Cache(typ string) (any, bool) {
	cli, ok := k.caches[typ]
	return cli, ok
}

// Caches 返回全部已装配后端缓存实例（副本，段名→实例），用于遍历诊断。
func (k *AppKit) Caches() map[string]any {
	out := make(map[string]any, len(k.caches))
	for typ, cli := range k.caches {
		out[typ] = cli
	}
	return out
}
