// provider.go —— 段驱动装配的加法式 Provider 导出（生态接入，
// 范本 contrib/store-gorm 的 AppProvider）。既有构造逻辑（Provider 注册
// func，Init + Connect）保留不动；本文件只**新增**对 pkg/appkit 段驱动装配
// 的接入面：AppProvider() 消费契约 broker.redis 段，复用 Provider 的构造
// 逻辑，产物落引擎 broker 资源面，cleanup 断开连接。
//
// 放置说明（与 store-gorm 的结构差异）：本模块的契约映射逻辑在 contract
// 包（redis 包保持零契约依赖），root 包反向 import 本包——段驱动导出只能
// 落在这里，复用既有 Provider 而非复制映射。
package contract

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/pkg/appkit"
)

// AppProvider 返回消费契约 broker.redis 段的 appkit.Provider（段路径
// "broker.redis"；段缺失即被引擎跳过，合法）。
//
// 命名说明（store-gorm 同例）：本包既有 Provider（bootstrap.BrokerProvider
// 形状的注册 func）占用 Provider 标识符且保留不动，故段驱动装配的导出名
// 取 AppProvider。
//
// Build 语义：broker.redis 段在 → 经既有 Provider 构造（address 必填、
// Init/Connect fail-fast）→ 产物 map[string]any{"redis": broker} 落引擎
// broker 资源面 → cleanup 断开连接（停机逆序回放：in-flight 消息先于
// workflow/ai/storage/cache/database 关闭，服务器 drain 最先）。
func AppProvider() appkit.Provider { return appProvider{} }

// appProvider 消费 broker.redis 段。
type appProvider struct{}

func (appProvider) Segments() []string { return []string{"broker.redis"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
func (appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, appProvider{}) }

// Plan 声明 broker 节点：阶段 B、canonical broker 优先级（停机 Effect 名与
// 旧手工表一致）。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:        "broker.redis",
		Kind:       "broker",
		Segments:   []string{"broker.redis"},
		Phase:      appkit.PhaseBootstrap,
		Priority:   appkit.PriorityBroker,
		Reload:     appkit.ReloadRestart,
		EffectName: "appkit:broker-clients",
	}}, nil
}

// Build 复用既有 Provider 构造 Redis Broker 并落资源容器。
func (appProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	brokerSec := s.Config.GetBroker()
	if brokerSec == nil {
		return nil, nil, fmt.Errorf("broker/redis: broker section is missing " +
			"(provider matched by segment path; report this as a bug)")
	}
	b, cleanup, err := Provider(ctx, brokerSec)
	if err != nil {
		return nil, nil, err
	}
	return map[string]any{"redis": b}, adaptCleanup(cleanup), nil
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
