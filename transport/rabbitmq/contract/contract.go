// Package contract 提供 RabbitMQ 传输层的契约装配：bootstrapv1.Server 的
// rabbitmq 段 → rabbitmq.NewServer + 构造 func（供 server.rabbitmq 段
// Provider 的 Build 复用）。
//
// 单独成包的原因（与 transport/{tcp,graphql,kafka,redis}/contract 同惯例）：
// transport/rabbitmq 保持零契约依赖（核心不 import bconf），只有本包 import
// bconf——业务按需 import，依赖图全程可见。
//
// 装配序列本体（校验→构造→afterBuild）住在 transport/internal/brokercontract
// 共享 kernel（J2 单一闸门——此前与 kafka 逐行克隆）；本包保留家族参数化点
// （段键/文案/选项构造器/工厂）与类型面（WithServerOptions/WithAfterBuild）。
//
// 契约段两个字段全部可映射（addrs / codec）；exchange、TLS、外部鉴权、metrics
// 等代码侧能力经 WithServerOptions 注入，消费者注册（RegisterSubscriber）经
// WithAfterBuild 注入。
//
// 与 kafka/redis 工厂同形（deferred 订阅 map、Endpoint 启动后描述性），后端差异
// 决定判据形状：rabbitmq broker 的 Connect 走完整 AMQP 0-9-1 握手——进程内
// 假后端不可行（协议帧面远大于 RESP 那层），所以「Start 成功后端点变描述性」
// 这一态离线不可判，归真集群/CI service 集成判据。本包能离线判的是：
//   - addr 与 codec 行的 fail-fast（配段不给地址/空元素/未注册 codec）；
//   - 地址映射的线上形状：Start 尝试在拒连端口上必红且 started 不落真、
//     端点保持空（拨号目标就是契约地址本身——错映射会拨到别处或绿）；
//   - 业务覆盖序（末者胜）在同一 Start 失败形状上可观测。
package contract

import (
	"context"
	"fmt"


	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport"
	"github.com/kalandramo/bald/transport/internal/brokercontract"
	"github.com/kalandramo/bald/transport/rabbitmq"
)

// Type 是契约 server 段中 rabbitmq 后端的段名。
const Type = "rabbitmq"

// Option 配置本 Provider 的行为（非 RabbitMQ 服务器的 Option——那是
// rabbitmq.ServerOption）。
type Option func(*providerConfig)

type providerConfig struct {
	// serverOpts 是「server 构造前」应用的 rabbitmq.ServerOption（如 exchange、
	// TLS、外部鉴权、metrics）。
	serverOpts []rabbitmq.ServerOption
	// afterBuild 是「server 构造后」执行的回调（如业务侧接线：注册消息处理器）。
	// 返回 error 则装配失败并短路。
	afterBuild []func(context.Context, *rabbitmq.Server) error
}

// WithServerOptions 追加 rabbitmq.ServerOption——覆盖契约段表达不了的能力。
//
// transport/rabbitmq/options.go 的全部 ServerOption 均可经此传入（exchange、
// TLS、metrics 等）。追加项在契约字段**之后**应用，故可覆盖契约值——
// broker.Options.Apply 末者胜，业务 WithAddress 会顶掉契约 addrs。
func WithServerOptions(opts ...rabbitmq.ServerOption) Option {
	return func(c *providerConfig) { c.serverOpts = append(c.serverOpts, opts...) }
}

// WithAfterBuild 注册「server 构造后」的回调，用于业务侧接线。
//
// **时序**：回调在 rabbitmq.NewServer 之后、返回 server 之前执行——早于 appkit
// 的 Start。Start 前 RegisterSubscriber 只进 deferred map；订阅真正上线发生在
// Start 的 flush，而 flush 排在 Connect 之后——离线（Connect 握手必红）时
// deferred map 不会被消费，「登记的订阅递进 SDK」属真集群判据。
func WithAfterBuild(fn func(context.Context, *rabbitmq.Server) error) Option {
	return func(c *providerConfig) { c.afterBuild = append(c.afterBuild, fn) }
}

// Provider 返回契约驱动的 RabbitMQ 工厂函数。
//
// 返回**未命名函数类型**——调用方在 server.rabbitmq 段 Provider 的 Build 中
// 直接调用，无需显式类型转换，本包也不必 import bootstrap。
//
// **addrs 必填**：段存在而地址为空（或全部为空串元素）时 fail-fast——
// broker 的 Init 会丢弃空元素并整体兜底到内置默认 amqp://guest:guest@
// 127.0.0.1:5672（broker/rabbitmq/rabbitmq.go:52-67），一条「配置写了、落点
// 不由配置决定」的静默失效路径，而且默认值自带 guest 凭证，契约侧不接受空值。
//
// **codec 未注册即报错**：broker.WithCodec 对未注册的名字静默置 nil，首条
// marshal 才炸 errNoCodec；契约字段非空即查注册表，装配期报错比首包诚实。
func Provider(opts ...Option) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	c := &providerConfig{}
	for _, o := range opts {
		o(c)
	}
	// 家族类型面 → kernel 的 transport.Server 面：NewServer 产物即
	// *rabbitmq.Server，断言恒成立（adapter 的 afterBuild 通道是统一形状）。
	afterBuild := make([]func(context.Context, transport.Server) error, 0, len(c.afterBuild))
	for _, fn := range c.afterBuild {
		fn := fn
		afterBuild = append(afterBuild, func(ctx context.Context, srv transport.Server) error {
			return fn(ctx, srv.(*rabbitmq.Server))
		})
	}
	return func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		return factory().Build(ctx, cfg, c.serverOpts, afterBuild)
	}
}

// factory 是 rabbitmq 家族的契约装配参数化点（序列本体在 brokercontract kernel）。
// 空元素文案带下标并点名 amqp 内置默认值（自带 guest 凭证——比 kafka 的文案
// 多一档风险说明）。
func factory() brokercontract.Factory[rabbitmq.ServerOption] {
	return brokercontract.Factory[rabbitmq.ServerOption]{
		Kind: Type,
		Key:  "server.rabbitmq",
		Section: func(cfg *bootstrapv1.Server) brokercontract.Segment {
			if s := cfg.GetRabbitmq(); s != nil {
				return s
			}
			return nil // typed-nil 守卫：nil 具体指针装进接口后 sec == nil 不成立
		},
		EmptyAddrErr: func(i int) error {
			return fmt.Errorf("rabbitmq: server.rabbitmq.addrs[%d] is empty (broker Init drops empty entries and would fall back to the built-in amqp://guest:guest@127.0.0.1:5672; drop the empty element or the section)", i)
		},
		WithAddrs: func(addrs []string) rabbitmq.ServerOption { return rabbitmq.WithAddress(addrs) },
		WithCodec: func(codec string) rabbitmq.ServerOption { return rabbitmq.WithCodec(codec) },
		NewServer: func(opts ...rabbitmq.ServerOption) transport.Server { return rabbitmq.NewServer(opts...) },
	}
}

// 类型守卫：Provider 保持构造 func 形状（供 server.rabbitmq 段 Provider 的
// Build 调用；不在本包 import bootstrap——避免该依赖）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
