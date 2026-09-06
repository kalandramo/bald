// cache.go 实现缓存客户端的契约装配注册表（bootstrapv1.Cache 段 →
// 具体后端实例）。
//
// 与 DatabaseRegistry 同模式：显式注册（不用 init()+blank import，主程序
// 在 main() 里逐个 MustRegister）；段存在但未注册 fail-fast；cleanup 挂
// 停机 Effect 逆序回放。契约 cache 是 optional 段集合（local/redis 可
// 并存），装配语义是「每段各自构建、全部返回」。
//
// 与 contrib/cache-redis（Cache-Aside 旁路缓存，P11 晋升组件）的边界：
// 本层是通用 KV 缓存抽象（Get/Set/SetNX/Multi，进程内/分布式），面向
// 任意键值加速；cache-redis 是带 loader 回填的业务旁路缓存组件，长在
// 具体后端之上。关注点不同，互不替代。
//
// 生命周期：阶段 B（BeforeStart，契约装载校验后）构建；cache 段不支持
// 热更新（与 database 段同理）。停机 Effect 注册在 database-clients
// 之后——逆序回放保证「缓存先关（加速层，关了不影响正确性）、数据库
// 连接最后关」。
//
// 用法：
//
//	cr := appkit.NewCacheRegistry()
//	cr.MustRegister(localcontract.Type, localcontract.Provider)  // "local"
//	cr.MustRegister(rediscontract.Type, rediscontract.Provider)  // "redis"
//	app, err := appkit.FromBootstrap(cfg, appkit.WithCacheRegistry(cr), ...)
//	// Run 后取实例：
//	c := app.Cache(rediscontract.Type).(cache.Cache)
package appkit

import (
	"context"
	"errors"
	"fmt"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// CacheProvider 按契约 Cache 段构造具体后端缓存实例。
// 返回 (实例, cleanup, error)：cleanup 释放后端资源（可为 nil），由
// FromBootstrap 在停机 Effect 中逆序回放。
// 各后端的实现见 cache/<backend>/contract 包。
type CacheProvider func(ctx context.Context, cfg *bootstrapv1.Cache) (any, func(), error)

// CacheRegistry 是缓存后端 Provider 的显式注册表，键为契约段名。
// 实例非并发安全（main 装配期使用）；Build 线程安全。
type CacheRegistry struct {
	mu        sync.Mutex
	providers map[string]CacheProvider
}

// NewCacheRegistry 创建空注册表。
func NewCacheRegistry() *CacheRegistry {
	return &CacheRegistry{providers: make(map[string]CacheProvider)}
}

// Register 注册一个后端 Provider；重名/空名/nil provider 均 fail-fast。
func (r *CacheRegistry) Register(typ string, p CacheProvider) error {
	if typ == "" {
		return errors.New("appkit: cache type is empty")
	}
	if p == nil {
		return fmt.Errorf("appkit: cache provider for %q is nil", typ)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]CacheProvider)
	}
	if _, dup := r.providers[typ]; dup {
		return fmt.Errorf("appkit: cache provider %q already registered", typ)
	}
	r.providers[typ] = p
	return nil
}

// MustRegister 注册后端 Provider，失败 panic（装配期错误应当炸在启动时）。
func (r *CacheRegistry) MustRegister(typ string, p CacheProvider) {
	if err := r.Register(typ, p); err != nil {
		panic(err)
	}
}

// cacheSection 契约段枚举：固定顺序即装配顺序（proto 字段序），
// 新后端在 bconf 加段后在此追加。
type cacheSection struct {
	name   string
	exists func(*bootstrapv1.Cache) bool
}

var cacheSections = []cacheSection{
	{"local", func(c *bootstrapv1.Cache) bool { return c.GetLocal() != nil }},
	{"redis", func(c *bootstrapv1.Cache) bool { return c.GetRedis() != nil }},
}

// Build 按契约段装配全部已配置的缓存后端：段存在 → 查表构建。
// 全部段缺失为 no-op；任一段存在但未注册 Provider 均 fail-fast；
// 构建失败回滚已建实例的 cleanup（逆序）。
func (r *CacheRegistry) Build(ctx context.Context, cfg *bootstrapv1.Cache) (map[string]any, func(), error) {
	if cfg == nil {
		return nil, nil, nil // 未配置任何缓存
	}
	r.mu.Lock()
	provs := make(map[string]CacheProvider, len(r.providers))
	for k, v := range r.providers {
		provs[k] = v
	}
	r.mu.Unlock()

	var (
		clients  = make(map[string]any)
		cleanups []func()
		rollback = func() {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
		}
	)
	for _, sec := range cacheSections {
		if !sec.exists(cfg) {
			continue // 段缺失 = 未声明该后端
		}
		p, ok := provs[sec.name]
		if !ok {
			rollback()
			return nil, nil, fmt.Errorf("appkit: cache.%s present but provider not registered (import the backend contract package and MustRegister it)", sec.name)
		}
		cli, cleanup, err := p(ctx, cfg)
		if err != nil {
			rollback()
			return nil, nil, fmt.Errorf("appkit: build cache %s: %w", sec.name, err)
		}
		clients[sec.name] = cli
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	}
	aggregated := func() { rollback() }
	if len(cleanups) == 0 {
		aggregated = nil
	}
	return clients, aggregated, nil
}

// WithCacheRegistry 注入缓存后端的契约装配注册表（显式注册 provider，
// 见 CacheRegistry）。契约 cache 段任一后端段存在时，阶段 B 按段名查表
// 构建缓存实例；cache 段不支持热更新。
func WithCacheRegistry(r *CacheRegistry) BootstrapOption {
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
