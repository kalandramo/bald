// broker.go 是 appkit 侧的消息代理装配胶水：契约 broker 段的 Registry
// （构造期）已迁 bootstrap（2026-09-15，见 bootstrap/broker.go），本文件
// 保留运行期装配面——WithBrokerRegistry Option、buildBrokers 胶水、
// Broker/Brokers 访问器。
//
// 分工（构造期归 bootstrap，运行期归 appkit）：
//   - bootstrap.BrokerRegistry：显式注册 Provider、段枚举、Build
//     （先 Init 再 Connect，各后端 contract 已封装）；
//   - appkit：BeforeStart（阶段 B，契约装载校验后）调 Build，实例存
//     a.brokers；cleanup 挂停机 Effect——注册在 workflow-clients 之后，
//     停机逆序回放时 broker（in-flight 消息）先于 workflow/ai/storage/
//     cache/database 关闭，服务器 drain 最先。broker 段不支持热更新。
//
// 用法：
//
//	br := bootstrap.NewBrokerRegistry()
//	br.MustRegister(kafkacontract.Type, kafkacontract.Provider)         // "kafka"
//	br.MustRegister(rabbitmqcontract.Type, rabbitmqcontract.Provider)   // "rabbitmq"
//	app, err := appkit.FromBootstrap(cfg, appkit.WithBrokerRegistry(br), ...)
//	// Run 后取实例：
//	b   := app.Broker(kafkacontract.Type).(*kafka.KafkaBroker)
//	pub := app.Publish(kafkacontract.Type, "events", &broker.Message{...})
package appkit

import (
	"context"
	"errors"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
)

// WithBrokerRegistry 注入消息代理后端的契约装配注册表（显式注册 provider，
// 见 bootstrap.BrokerRegistry）。契约 broker 段任一后端段存在时，阶段 B
// 按段名查表构建实例；broker 段不支持热更新。
func WithBrokerRegistry(r *baldbootstrap.BrokerRegistry) BootstrapOption {
	return func(s *bootstrapSpec) { s.brokerRegistry = r }
}

// buildBrokers 在阶段 B（契约装载校验后）按契约 broker 段构建全部已配置
// 后端的消息代理实例，存入 a.brokers 供业务经 Broker/Brokers 取用。
// broker 段缺失为 no-op；段存在但未接 BrokerRegistry 则 fail-fast。
// 返回聚合 cleanup（可为 nil），由 FromBootstrap 挂停机 Effect。
func buildBrokers(a *AppKit, cfg *bootstrapv1.BootstrapConfig, spec *bootstrapSpec) (func(), error) {
	bcfg := cfg.GetBroker()
	if bcfg == nil {
		return nil, nil // 未配置任何消息代理
	}
	if spec.brokerRegistry == nil {
		return nil, errors.New("appkit: bootstrap.broker present but no BrokerRegistry wired (WithBrokerRegistry missing)")
	}
	clients, cleanup, err := spec.brokerRegistry.Build(context.Background(), bcfg)
	if err != nil {
		return nil, err
	}
	a.brokers = clients
	return cleanup, nil
}

// Broker 返回按契约段装配的消息代理实例（阶段 B 后、Run 期可用；
// FromBootstrap 构造期尚未装载契约，返回空）。
// typ 为契约段名（见 broker/<impl>/contract.Type，如 "kafka"）。
// 并发语义：brokers 只在 BeforeStart（阶段 B）单次写入，Run 后只读，
// happens-before 由 Run 启动流程保证。
func (k *AppKit) Broker(typ string) (any, bool) {
	cli, ok := k.brokers[typ]
	return cli, ok
}

// Brokers 返回全部已装配消息代理实例（副本，段名→实例），用于遍历诊断。
func (k *AppKit) Brokers() map[string]any {
	out := make(map[string]any, len(k.brokers))
	for typ, cli := range k.brokers {
		out[typ] = cli
	}
	return out
}
