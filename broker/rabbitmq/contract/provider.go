// provider.go —— 段驱动装配的加法式 Provider 导出（生态接入，范本
// broker/redis/contract 的 AppProvider 与 contrib/store-gorm）。既有构造逻辑
// （Provider 注册 func，Init + Connect）保留不动；本文件只**新增**对
// pkg/appkit 段驱动装配的接入面：AppProvider() 消费契约 broker.rabbitmq 段，
// 复用 Provider 的构造逻辑，产物落引擎 broker 资源面，cleanup 断开连接。
package contract

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/pkg/appkit"
)

// AppProvider 返回消费契约 broker.rabbitmq 段的 appkit.Provider（段路径
// "broker.rabbitmq"；段缺失即被引擎跳过，合法）。
//
// 命名说明（store-gorm 同例）：本包既有 Provider（契约段构造 func）占用
// Provider 标识符且保留不动，故段驱动装配的导出名取 AppProvider。
//
// Build 语义：broker.rabbitmq 段在 → 经既有 Provider 构造（连接 fail-fast）
// → 产物 map[string]any{"rabbitmq": broker} 落引擎 broker 资源面 → cleanup
// 断开连接（停机逆序回放）。
func AppProvider() appkit.Provider { return appProvider{} }

// appProvider 消费 broker.rabbitmq 段。
type appProvider struct{}

func (appProvider) Segments() []string { return []string{"broker.rabbitmq"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
func (appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, appProvider{}) }

// Plan 声明 broker 节点：阶段 B、canonical broker 优先级（停机 Effect 名与
// 旧手工表一致）。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:        "broker.rabbitmq",
		Kind:       "broker",
		Segments:   []string{"broker.rabbitmq"},
		Phase:      appkit.PhaseBootstrap,
		Priority:   appkit.PriorityBroker,
		Reload:     appkit.ReloadRestart,
		EffectName: "appkit:broker-clients",
	}}, nil
}

// Build 复用既有 Provider 构造 RabbitMQ Broker 并落资源容器。
func (appProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	brokerSec := s.Config.GetBroker()
	if brokerSec == nil {
		return nil, nil, fmt.Errorf("broker/rabbitmq: broker section is missing " +
			"(provider matched by segment path; report this as a bug)")
	}
	b, cleanup, err := Provider(ctx, brokerSec)
	if err != nil {
		return nil, nil, err
	}
	return map[string]any{"rabbitmq": b}, appkit.CleanupFrom(cleanup), nil
}

// 编译期断言：appProvider 满足 appkit.Provider（含 Option 嵌入面）。
var _ appkit.Provider = appProvider{}

// RabbitMQKey 是 broker.rabbitmq 段产物的标准资源键（ADR-16）：map 形产物按
// "<top>.<子段>" 展开为 broker.rabbitmq.rabbitmq——消费者直接引用本常量，
// 不再手写魔法串。appkit.NewKey[broker.Broker] 的类型化包装。
var RabbitMQKey = appkit.NewKey[broker.Broker]("broker.rabbitmq.rabbitmq")
