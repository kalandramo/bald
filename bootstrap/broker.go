package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// BrokerProvider 按契约 Broker 段构造具体消息代理实例（已 Connect）。
// 返回 (实例, cleanup, error)：cleanup 断开连接并清理订阅者，
// 由调用方释放（appkit FromBootstrap 挂停机 Effect 逆序回放）。
// 各后端的实现见 broker/<impl>/contract 包。
type BrokerProvider func(ctx context.Context, cfg *bootstrapv1.Broker) (any, func(), error)

// BrokerRegistry 是消息代理后端 Provider 的显式注册表，键为契约段名。
// 实例非并发安全（main 装配期使用）；Build 线程安全。
//
// 与 AiRegistry 同模式：显式注册（不用 init()+blank import，主程序在 main()
// 里逐个 MustRegister）；段存在但未注册 fail-fast。
// broker 是 optional 段集合（kafka/rabbitmq/redis/rocketmq 可并存），
// 装配语义是「每段各自构建、全部返回」——事件总线与任务队列可分别落在
// 不同 broker 上。
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
		return errors.New("bootstrap: broker type is empty")
	}
	if p == nil {
		return fmt.Errorf("bootstrap: broker provider for %q is nil", typ)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]BrokerProvider)
	}
	if _, dup := r.providers[typ]; dup {
		return fmt.Errorf("bootstrap: broker provider %q already registered", typ)
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

// brokerSection 契约段枚举：固定顺序即装配顺序（proto 字段序）。
//
// implemented=false 的段在 bconf 契约里有声明（超集契约：先声明后实现），
// 但本仓没有对应后端实现。它们**不是**「静默跳过」——配了未实现段会被
// Build fail-fast 拒绝，否则用户以为消息代理接上了、实际什么都没发生。
// 新后端实现落地后，把对应段的 implemented 改为 true 并在此登记。
type brokerSection struct {
	name        string
	implemented bool
	exists      func(*bootstrapv1.Broker) bool
}

var brokerSections = []brokerSection{
	{"kafka", true, func(b *bootstrapv1.Broker) bool { return b.GetKafka() != nil }},
	{"rabbitmq", true, func(b *bootstrapv1.Broker) bool { return b.GetRabbitmq() != nil }},
	{"redis", true, func(b *bootstrapv1.Broker) bool { return b.GetRedis() != nil }},
	{"nats", false, func(b *bootstrapv1.Broker) bool { return b.GetNats() != nil }},
	{"mqtt", false, func(b *bootstrapv1.Broker) bool { return b.GetMqtt() != nil }},
	{"pulsar", false, func(b *bootstrapv1.Broker) bool { return b.GetPulsar() != nil }},
	{"azuresb", false, func(b *bootstrapv1.Broker) bool { return b.GetAzuresb() != nil }},
	{"gcpubsub", false, func(b *bootstrapv1.Broker) bool { return b.GetGcpubsub() != nil }},
	{"nsq", false, func(b *bootstrapv1.Broker) bool { return b.GetNsq() != nil }},
	{"rocketmq", true, func(b *bootstrapv1.Broker) bool { return b.GetRocketmq() != nil }},
	{"sqs", false, func(b *bootstrapv1.Broker) bool { return b.GetSqs() != nil }},
	{"stomp", false, func(b *bootstrapv1.Broker) bool { return b.GetStomp() != nil }},
	{"activemq", false, func(b *bootstrapv1.Broker) bool { return b.GetActivemq() != nil }},
}

// Build 按契约段装配全部已配置的消息代理：段存在 → 查表构建。
// 全部段缺失为 no-op；任一段存在但未实现（契约超集里声明、本仓无后端）
// 或未注册 Provider 均 fail-fast；构建失败回滚已建实例的 cleanup（逆序）。
//
// 生命周期：由 appkit FromBootstrap 在阶段 B（BeforeStart，契约装载校验后）
// 调用，先 Init 再 Connect（各后端 contract 已封装）；broker 段不支持热更新
// （与 database 段同理）。cleanup 交还调用方，appkit 挂停机 Effect——注册在
// workflow-clients 之后，停机逆序回放时 broker（in-flight 消息）先于
// workflow/ai/storage/cache/database 关闭，服务器 drain 最先。
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
		if !sec.implemented {
			rollback()
			return nil, nil, fmt.Errorf("bootstrap: broker.%s is declared in the contract but not implemented by any backend (implement it or remove the section from config)", sec.name)
		}
		p, ok := provs[sec.name]
		if !ok {
			rollback()
			return nil, nil, fmt.Errorf("bootstrap: broker.%s present but provider not registered (import the backend contract package and MustRegister it)", sec.name)
		}
		cli, cleanup, err := p(ctx, cfg)
		if err != nil {
			rollback()
			return nil, nil, fmt.Errorf("bootstrap: build broker %s: %w", sec.name, err)
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
