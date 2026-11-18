// Package contract 提供 Kafka 传输层的契约装配：bootstrapv1.Server 的 kafka 段 →
// kafka.NewServer + 构造 func（供 server.kafka 段 Provider 的 Build 复用）。
//
// 单独成包的原因（与 transport/{sse,cron,asynq,gateway,tcp,websocket,http3,graphql,webrtc,mcp}/contract
// 同惯例）：transport/kafka 保持零契约依赖（核心不 import bconf），只有本包
// import bconf——业务按需 import，依赖图全程可见。
//
// 装配序列本体（校验→构造→afterBuild）住在 transport/internal/brokercontract
// 共享 kernel（J2 单一闸门——此前与 rabbitmq 逐行克隆）；本包保留家族参数化点
// （段键/文案/选项构造器/工厂）与类型面（WithServerOptions/WithAfterBuild）。
//
// 契约段两个字段全部可映射（addrs / codec）；TLS、SASL 凭证、metrics、注入自定义
// Broker 等代码侧能力经 WithServerOptions 注入，消费者注册（RegisterSubscriber）
// 经 WithAfterBuild 注入。
//
// 与 broker 型服务器的接线形状（区别于 HTTP 型工厂的关键两点，判据按此设计）：
//   - kafka 的 Start 是**非阻塞**的 Init+Connect（消费跑在 broker 内部 goroutine），
//     且 segment 驱动的 readiness 走 Endpoint()——kafka.Server 的 Endpoint 在启动后
//     返回描述性 `kafka://<主 broker 地址>`（asynq 同形，见 transport/kafka/server.go）。
//   - 消费者注册表是 Server **内部的 deferred map**：Start 前任意时刻
//     RegisterSubscriber 都只登记不触网，Start 时统一 flush——所以 handler 不需要
//     构造入参（gateway 的位置参数 RegisterFunc 形状在这里是不必要的）。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport"
	"github.com/kalandramo/bald/transport/internal/brokercontract"
	"github.com/kalandramo/bald/transport/kafka"
)

// Type 是契约 server 段中 kafka 后端的段名。
const Type = "kafka"

// Option 配置本 Provider 的行为（非 Kafka 服务器的 Option——那是 kafka.ServerOption）。
type Option func(*providerConfig)

type providerConfig struct {
	// serverOpts 是「server 构造前」应用的 kafka.ServerOption（如 TLS、SASL、metrics、自定义 Broker）。
	serverOpts []kafka.ServerOption
	// afterBuild 是「server 构造后」执行的回调（如业务侧接线：注册消息处理器）。
	// 返回 error 则装配失败并短路。
	afterBuild []func(context.Context, *kafka.Server) error
}

// WithServerOptions 追加 kafka.ServerOption——覆盖契约段表达不了的能力。
//
// transport/kafka/options.go 的全部 ServerOption 均可经此传入（TLS、SASL 机制、
// metrics、自定义 Broker 等）。追加项在契约字段**之后**应用，故可覆盖契约值
// ——broker.Options.Apply 末者胜，所以业务 WithAddress 会顶掉契约 addrs。
func WithServerOptions(opts ...kafka.ServerOption) Option {
	return func(c *providerConfig) { c.serverOpts = append(c.serverOpts, opts...) }
}

// WithAfterBuild 注册「server 构造后」的回调，用于业务侧接线。
//
// **时序**：回调在 kafka.NewServer 之后、返回 server 之前执行——早于 appkit 的
// Start。注册消费者既可以在这里（deferred map 在 Start 时 flush），也可以在
// Start 之后（RegisterSubscriber 走 live Subscribe 通道）；这里执行只保证
// 「段存在 → 消费者的确登记上了」。
func WithAfterBuild(fn func(context.Context, *kafka.Server) error) Option {
	return func(c *providerConfig) { c.afterBuild = append(c.afterBuild, fn) }
}

// Provider 返回契约驱动的 Kafka 工厂函数。
//
// 返回**未命名函数类型**——调用方在 server.kafka 段 Provider 的 Build 中直接
// 调用，无需显式类型转换，本包也不必 import bootstrap。
//
// **addrs 必填且每一项非空**：段存在但 addrs 为空或含空串时 fail-fast——
// broker/kafka 的 Init 会把两者都归一成 `defaultAddr = "127.0.0.1:9092"`
// （broker/kafka/kafka.go:18, 98-110），一条「配置写了、落点不由配置决定」的
// 静默失效路径，所以契约侧不接受空值。
//
// **codec 未注册即报错**：broker.WithCodec 对未注册的名字静默置 nil
// （broker/options.go:124-131 → encoding/encoding.go:49-57），首条 marshal 才
// 炸（errNoCodec）；契约字段非空即查注册表，装配期报错比首包诚实。
//
// **无 tls 契约通道**：server.proto 的 Kafka 子段没有 tls 字段（与内置 http/grpc
// 的服务器段不同）。TLS 与 SASL 只能经 WithServerOptions 注入——这与契约形状一致，
// 不是缺口（见风险评审 §7.28 第三批条）。
func Provider(opts ...Option) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	c := &providerConfig{}
	for _, o := range opts {
		o(c)
	}
	// 家族类型面 → kernel 的 transport.Server 面：NewServer 产物即 *kafka.Server，
	// 断言恒成立（adapter 的 afterBuild 通道是统一形状）。
	afterBuild := make([]func(context.Context, transport.Server) error, 0, len(c.afterBuild))
	for _, fn := range c.afterBuild {
		fn := fn
		afterBuild = append(afterBuild, func(ctx context.Context, srv transport.Server) error {
			return fn(ctx, srv.(*kafka.Server))
		})
	}
	return func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		return factory().Build(ctx, cfg, c.serverOpts, afterBuild)
	}
}

// factory 是 kafka 家族的契约装配参数化点（序列本体在 brokercontract kernel）。
// 空元素文案点名 kafka 的内置默认地址（127.0.0.1:9092），不带下标。
func factory() brokercontract.Factory[kafka.ServerOption] {
	return brokercontract.Factory[kafka.ServerOption]{
		Kind: Type,
		Key:  "server.kafka",
		Section: func(cfg *bootstrapv1.Server) brokercontract.Segment {
			if s := cfg.GetKafka(); s != nil {
				return s
			}
			return nil // typed-nil 守卫：nil 具体指针装进接口后 sec == nil 不成立
		},
		EmptyAddrErr: func(int) error {
			return fmt.Errorf("kafka: server.kafka.addrs must not contain empty entries (would fall back to the built-in default 127.0.0.1:9092; drop the empty element or the section)")
		},
		WithAddrs: func(addrs []string) kafka.ServerOption { return kafka.WithAddress(addrs) },
		WithCodec: func(codec string) kafka.ServerOption { return kafka.WithCodec(codec) },
		NewServer: func(opts ...kafka.ServerOption) transport.Server { return kafka.NewServer(opts...) },
	}
}

// 类型守卫：Provider 保持构造 func 形状（供 server.kafka 段 Provider 的
// Build 调用；不在本包 import bootstrap——避免该依赖）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
