// Package contract 提供 Redis 传输层的契约装配：bootstrapv1.Server 的 redis_server
// 段 → redis.NewServer + 构造 func（供 server.redis_server 段 Provider 的 Build 复用）。
//
// 单独成包的原因（与 transport/{tcp,graphql,kafka}/contract 同惯例）：
// transport/redis 保持零契约依赖（核心不 import bconf），只有本包 import
// bconf——业务按需 import，依赖图全程可见。
//
// 契约段两个字段全部可映射（addr / codec）；TLS、连接超时、Stream 驱动
// （WithDriverType）、metrics、注入自定义 Broker 等代码侧能力经 WithServerOptions
// 注入，消费者注册（RegisterSubscriber）经 WithAfterBuild 注入。
//
// 与 kafka 工厂同形（deferred 订阅 map、Start 同步非阻塞、Endpoint 启动后
// 描述性），两处后端差异决定判据形状：
//   - pubsub broker 的 Connect **真探活**（取连接做 PING，失败即报错并丢池）
//     ——Start 成功当且仅当后端应答 RESP；离线判据要么用进程内假 RESP，要么
//     判「拒连端口上 Start 必红」，没有 kafka 那种「无集群也能 Start 绿」的中间态。
//   - pubsub 的 Address() 在 Init 后是规范化 URL（缺 scheme 自动补 redis://，
//     缺地址落默认 redis://127.0.0.1:6379）——默认兜底是「落点不由配置决定」的
//     静默失效路径，所以 addr 为空必须契约侧 fail-fast。
package contract

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/encoding"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport"
	redis "github.com/kalandramo/bald/transport/redis"
)

// Type 是契约 server 段中 redis 后端的段名。
const Type = "redis"

// Option 配置本 Provider 的行为（非 Redis 服务器的 Option——那是 redis.ServerOption）。
type Option func(*providerConfig)

type providerConfig struct {
	// serverOpts 是「server 构造前」应用的 redis.ServerOption（如 TLS、超时、
	// Stream 驱动、metrics、自定义 Broker）。
	serverOpts []redis.ServerOption
	// afterBuild 是「server 构造后」执行的回调（如业务侧接线：注册消息处理器）。
	// 返回 error 则装配失败并短路。
	afterBuild []func(context.Context, *redis.Server) error
}

// WithServerOptions 追加 redis.ServerOption——覆盖契约段表达不了的能力。
//
// transport/redis/options.go 的全部 ServerOption 均可经此传入（TLS、各类超时、
// WithDriverType(stream) 切 Stream 驱动、metrics 等）。追加项在契约字段**之后**
// 应用，故可覆盖契约值——broker.Options.Apply 末者胜，业务 WithAddress 会顶掉契约 addr。
func WithServerOptions(opts ...redis.ServerOption) Option {
	return func(c *providerConfig) { c.serverOpts = append(c.serverOpts, opts...) }
}

// WithAfterBuild 注册「server 构造后」的回调，用于业务侧接线。
//
// **时序**：回调在 redis.NewServer 之后、返回 server 之前执行——早于 appkit 的
// Start。Start 前 RegisterSubscriber 只进 deferred map（Start 时统一 flush），
// Start 后走 live Subscribe 通道；这里执行只保证「段存在 → 消费者的确登记上了」。
func WithAfterBuild(fn func(context.Context, *redis.Server) error) Option {
	return func(c *providerConfig) { c.afterBuild = append(c.afterBuild, fn) }
}

// Provider 返回契约驱动的 Redis 工厂函数。
//
// 返回**未命名函数类型**——调用方在 server.redis_server 段 Provider 的 Build 中
// 直接调用，无需显式类型转换，本包也不必 import bootstrap。
//
// 仅当契约 server.redis_server 段存在时构造（段缺失返回 nil server，引擎跳过）。
//
// **addr 必填**：段存在而 addr 为空时 fail-fast——pubsub broker 的 Init 会兜底
// 到 `redis://127.0.0.1:6379`（broker/redis/pubsub/redis.go:70-74），一条「配置
// 写了、落点不由配置决定」的静默失效路径，所以契约侧不接受空值。
//
// **codec 未注册即报错**：broker.WithCodec 对未注册的名字静默置 nil
// （broker/options.go:124-131 → encoding/encoding.go:49-57），首条 marshal 才
// 炸（errNoCodec）；契约字段非空即查注册表，装配期报错比首包诚实。
//
// **无 tls / driver_type 契约通道**：server.proto 的 RedisServer 子段只有
// addr/codec 两字段。TLS 与 Stream 驱动只能经 WithServerOptions 注入——这与
// 契约形状一致，不是缺口（见风险评审 §7.28 第三批条）。
func Provider(opts ...Option) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	c := &providerConfig{}
	for _, o := range opts {
		o(c)
	}
	return func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		sec := cfg.GetRedisServer()
		if sec == nil {
			return nil, nil, nil // 未配置 redis_server 段，跳过
		}
		if addr := sec.GetAddr(); addr == "" {
			return nil, nil, fmt.Errorf("redis: server.redis_server.addr is required (remove the redis_server section to disable)")
		}

		// 契约字段在前，业务 Option 在后（后者可覆盖前者——与 kafka/tcp 同约定）。
		base, err := buildOpts(sec)
		if err != nil {
			return nil, nil, err
		}
		allOpts := append(base, c.serverOpts...) //nolint:gocritic // base 是本函数新建的定长切片

		srv := redis.NewServer(allOpts...)

		for _, fn := range c.afterBuild {
			if err := fn(ctx, srv); err != nil {
				return nil, nil, fmt.Errorf("redis: after-build hook: %w", err)
			}
		}
		return srv, nil, nil
	}
}

// buildOpts 把契约 redis_server 段映射为 redis.ServerOption 序列。
//
// addr 无条件传（工厂入口已挡空值）；codec 非空即查注册表，注册了才追加。
// addr 可以裸 host:port 也可以带 redis:// scheme——pubsub 的 Init 负责规范化，
// 本层不再判形状。
func buildOpts(sec *bootstrapv1.Server_RedisServer) ([]redis.ServerOption, error) {
	opts := []redis.ServerOption{redis.WithAddress(sec.GetAddr())}
	if codec := sec.GetCodec(); codec != "" {
		if encoding.GetCodec(codec) == nil {
			return nil, fmt.Errorf("redis: server.redis_server.codec %q is not registered (import the codec package and register it, e.g. encoding.MustRegister(jsoncodec.New()), or drop the codec field)", codec)
		}
		opts = append(opts, redis.WithCodec(codec))
	}
	return opts, nil
}

// 类型守卫：Provider 保持构造 func 形状（供 server.redis_server 段 Provider 的
// Build 调用；不在本包 import bootstrap——避免该依赖）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
