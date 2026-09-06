// broker.go 实现消息代理的契约装配注册表（bootstrapv1.Broker 段 →
// 具体后端实例）。
//
// 与 AiRegistry 同模式：显式注册（不用 init()+blank import，主程序
// 在 main() 里逐个 MustRegister）；段存在但未注册 fail-fast；cleanup 挂
// 停机 Effect 逆序回放。broker 是 optional 段集合（kafka/rabbitmq/redis/
// rocketmq 可并存），装配语义是「每段各自构建、全部返回」——事件总线与
// 任务队列可分别落在不同 broker 上。
//
// 生命周期：阶段 B（BeforeStart，契约装载校验后）构建，先 Init 再 Connect
// （各后端 contract 已封装）；broker 段不支持热更新（与 database 段同理）。
// 停机 Effect 注册在 workflow-clients 之后——停机逆序回放时 broker（in-flight
// 消息）先于 workflow/ai/storage/cache/database 关闭，服务器 drain 最先。
//
// 用法：
//
//	br := appkit.NewBrokerRegistry()
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
	"fmt"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// BrokerProvider 按契约 Broker 段构造具体消息代理实例（已 Connect）。
// 返回 (实例, cleanup, error)：cleanup 断开连接并清理订阅者，
// 由 FromBootstrap 在停机 Effect 中逆序回放。
// 各后端的实现见 broker/<impl>/contract 包。
type BrokerProvider func(ctx context.Context, cfg *bootstrapv1.Broker) (any, func(), error)

// BrokerRegistry 是消息代理后端 Provider 的显式注册表，键为契约段名。
// 实例非并发安全（main 装配期使用）；Build 线程安全。
type BrokerRegistry struct {
	mu        sync.Mutex
	providers map[string]BrokerProvider
}

// NewBrokerRegistry 创建空注册表。
func NewBrokerRegistry() *BrokerRegistry {
	return &BrokerRegistry{providers: make(map[string]BrokerProvider)}
}

// Register 注册一个后端 Provider；重名/空名/nil provider 均 fail-fast。
func (r *BrokerRegistry) Register(typ string, p BrokerProvider) error {
	if typ == "" {
		return errors.New("appkit: broker type is empty")
	}
	if p == nil {
		return fmt.Errorf("appkit: broker provider for %q is nil", typ)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]BrokerProvider)
	}
	if _, dup := r.providers[typ]; dup {
		return fmt.Errorf("appkit: broker provider %q already registered", typ)
	}
	r.providers[typ] = p
	return nil
}

// MustRegister 注册后端 Provider，失败 panic（装配期错误应当炸在启动时）。
func (r *BrokerRegistry) MustRegister(typ string, p BrokerProvider) {
	if err := r.Register(typ, p); err != nil {
		panic(err)
	}
}

// brokerSection 契约段枚举：固定顺序即装配顺序（proto 字段序），
// 新后端在 bconf 加段后在此追加。
type brokerSection struct {
	name   string
	exists func(*bootstrapv1.Broker) bool
}

var brokerSections = []brokerSection{
	{"kafka", func(b *bootstrapv1.Broker) bool { return b.GetKafka() != nil }},
	{"rabbitmq", func(b *bootstrapv1.Broker) bool { return b.GetRabbitmq() != nil }},
	{"redis", func(b *bootstrapv1.Broker) bool { return b.GetRedis() != nil }},
	{"rocketmq", func(b *bootstrapv1.Broker) bool { return b.GetRocketmq() != nil }},
}

// Build 按契约段装配全部已配置的消息代理：段存在 → 查表构建。
// 全部段缺失为 no-op；任一段存在但未注册 Provider 均 fail-fast；
// 构建失败回滚已建实例的 cleanup（逆序）。
func (r *BrokerRegistry) Build(ctx context.Context, cfg *bootstrapv1.Broker) (map[string]any, func(), error) {
	if cfg == nil {
		return nil, nil, nil // 未配置任何消息代理
	}
	r.mu.Lock()
	provs := make(map[string]BrokerProvider, len(r.providers))
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
	for _, sec := range brokerSections {
		if !sec.exists(cfg) {
			continue // 段缺失 = 未声明该后端
		}
		p, ok := provs[sec.name]
		if !ok {
			rollback()
			return nil, nil, fmt.Errorf("appkit: broker.%s present but provider not registered (import the backend contract package and MustRegister it)", sec.name)
		}
		cli, cleanup, err := p(ctx, cfg)
		if err != nil {
			rollback()
			return nil, nil, fmt.Errorf("appkit: build broker %s: %w", sec.name, err)
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

// WithBrokerRegistry 注入消息代理后端的契约装配注册表（显式注册 provider，
// 见 BrokerRegistry）。契约 broker 段任一后端段存在时，阶段 B 按段名查表
// 构建实例；broker 段不支持热更新。
func WithBrokerRegistry(r *BrokerRegistry) BootstrapOption {
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
