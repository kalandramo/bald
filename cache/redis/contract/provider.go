// provider.go —— 段驱动装配的加法式 Provider 导出（生态接入，
// 范本 contrib/store-gorm 的 AppProvider）。既有构造逻辑（Provider 注册
// func，validate + dial + Ping）保留不动；本文件只**新增**对 pkg/appkit
// 段驱动装配的接入面：AppProvider() 消费契约 cache.redis 段，复用 Provider
// 的构造逻辑，产物落引擎 cache 资源面，cleanup 关闭底层连接池。
//
// 接入形态（目标 main.go）：
//
//	appkit.MustServe(ctx,
//	    std.Standard(),
//	    rediscontract.AppProvider(),            // 契约 cache.redis → 资源面
//	    ...) // 域 Wire/业务钩子内经 rediscontract.Redis.Resource(app) 取用
//
// 放置说明（与 store-gorm 的结构差异）：本模块的契约映射逻辑在 contract
// 包（redis 包保持零契约依赖），root 包反向 import 本包——段驱动导出只能
// 落在这里，复用既有 Provider 而非复制映射。
package contract

import (
	"context"
	"fmt"

	rediscache "github.com/kalandramo/bald/cache/redis"
	"github.com/kalandramo/bald/pkg/appkit"
)

// Redis 是 cache.redis 资源的类型化句柄（消费侧经 Redis.Resource(app) 取
// *rediscache.Cache）。
var Redis = appkit.NewKey[*rediscache.Cache]("cache.redis")

// AppProvider 返回消费契约 cache.redis 段的 appkit.Provider（段路径
// "cache.redis"；段缺失即被引擎跳过，合法）。
//
// 命名说明（store-gorm 同例）：本包既有 Provider（契约段构造 func）占用
// Provider 标识符且保留不动，故段驱动装配的导出名取 AppProvider。
//
// Build 语义：cache.redis 段在 → 经既有 Provider 构造（validate/dial/Ping
// fail-fast，不产半成品实例）→ 产物 map[string]any{"redis": client} 落引擎
// cache 资源面（Run 后经 Redis.Resource(app) 取用）→ cleanup 关闭底层连接池（停机逆序
// 回放：缓存先关、数据库最后关）。
func AppProvider() appkit.Provider { return appProvider{} }

// appProvider 消费 cache.redis 段。
type appProvider struct{}

func (appProvider) Segments() []string { return []string{"cache.redis"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
func (appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, appProvider{}) }

// Plan 声明段节点（cache.redis；阶段由段族决定）。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:        "cache.redis",
		Segments:   []string{"cache.redis"},
		Phase:      appkit.PhaseBootstrap,
		Priority:   appkit.PriorityCache,
		Reload:     appkit.ReloadRestart,
		EffectName: "appkit:cache-clients",
	}}, nil
}

// Build 复用既有 Provider 构造 Redis 缓存并落资源容器。
func (appProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	cacheSec := s.Config.GetCache()
	if cacheSec == nil {
		return nil, nil, fmt.Errorf("cache/redis: cache section is missing " +
			"(provider matched by segment path; report this as a bug)")
	}
	cli, cleanup, err := Provider(ctx, cacheSec)
	if err != nil {
		return nil, nil, err
	}
	return map[string]any{"redis": cli}, adaptCleanup(cleanup), nil
}

// adaptCleanup 把既有无参清理闭包适配为 appkit.Cleanup（nil 透传：无资源）。
func adaptCleanup(cleanup func()) appkit.Cleanup {
	if cleanup == nil {
		return nil
	}
	return func(context.Context) error { cleanup(); return nil }
}

// 编译期断言：appProvider 满足 appkit.Provider（含 Option 嵌入面）。
var _ appkit.Provider = appProvider{}
