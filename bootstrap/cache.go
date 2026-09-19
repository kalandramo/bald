package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// CacheProvider 按契约 Cache 段构造具体后端缓存实例。
// 返回 (实例, cleanup, error)：cleanup 释放后端资源（可为 nil），由
// 调用方释放（appkit FromBootstrap 挂停机 Effect 逆序回放）。
// 各后端的实现见 cache/<backend>/contract 包。
type CacheProvider func(ctx context.Context, cfg *bootstrapv1.Cache) (any, func(), error)

// CacheRegistry 是缓存后端 Provider 的显式注册表，键为契约段名。
// 实例非并发安全（main 装配期使用）；Build 线程安全。
//
// 与 DatabaseRegistry 同模式：显式注册（不用 init()+blank import，主程序
// 在 main() 里逐个 MustRegister）；段存在但未注册 fail-fast。
// 契约 cache 是 optional 段集合（local/redis 可并存），装配语义是
// 「每段各自构建、全部返回」。
//
// 与业务旁路缓存（Cache-Aside）的分工：本层是通用 KV 缓存抽象
// （Get/Set/SetNX/Multi，进程内/分布式），面向任意键值加速；带 loader
// 回填的旁路语义由 cache/loadable 在本层之上组合（读穿透 + singleflight
// 合并并发 miss，WithDegradeOnError 可选降级）。原 contrib/cache-redis
// 组件已删除（2026-09-18，能力被 cache/redis + cache/loadable 覆盖）。
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
		return errors.New("bootstrap: cache type is empty")
	}
	if p == nil {
		return fmt.Errorf("bootstrap: cache provider for %q is nil", typ)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]CacheProvider)
	}
	if _, dup := r.providers[typ]; dup {
		return fmt.Errorf("bootstrap: cache provider %q already registered", typ)
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
//
// 生命周期：由 appkit FromBootstrap 在阶段 B（BeforeStart，契约装载校验后）
// 调用；cache 段不支持热更新（与 database 段同理）。cleanup 交还调用方，
// appkit 挂停机 Effect——注册在 database-clients 之后，逆序回放保证
// 「缓存先关（加速层，关了不影响正确性）、数据库连接最后关」。
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
			return nil, nil, fmt.Errorf("bootstrap: cache.%s present but provider not registered (import the backend contract package and MustRegister it)", sec.name)
		}
		cli, cleanup, err := p(ctx, cfg)
		if err != nil {
			rollback()
			return nil, nil, fmt.Errorf("bootstrap: build cache %s: %w", sec.name, err)
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
