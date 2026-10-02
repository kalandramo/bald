// Package contract 提供 asynq 任务队列的契约装配：bootstrapv1.Server 的
// asynq 段 → asynq.Option 映射 + 契约驱动的 ServerProvider。
//
// 单独成包的原因：transport/asynq 保持零契约依赖（纯 SDK 封装），只有本包
// import bconf——业务按需 import，依赖图全程可见。与 cache/redis/contract、
// broker/kafka/contract 同一模式。
//
// 与 bootstrap 的关系：本包**不** import bootstrap。Provider 的返回类型是
// 结构化兼容的自有函数类型，注册时反正地传入 bootstrap.ServerRegistry：
//
//	sr := baldbootstrap.NewServerRegistry()
//	sr.MustRegister(asynqcontract.Type, asynqcontract.Provider(...))
//
// appkit 侧经 WithServerRegistry 注入该注册表即可（见 pkg/appkit 注释）。
//
// 契约字段 vs 实现 Option 面：契约收「装配必需 + 高频」子集（Redis 连接、
// 并发、队列、codec、优雅关闭等），其余 ~46 个 Option 经 [WithServerOptions]
// 由业务补齐——契约表达声明式常用项，Option 表达完整能力。
package contract

import (
	"context"
	"fmt"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport"
	"github.com/kalandramo/bald/transport/asynq"
)

// Type 是契约 server 段中 asynq 后端的段名。
const Type = "asynq"

// ServerProvider 是契约驱动的服务器工厂签名（结构化兼容 bootstrap.ServerProvider）。
// 返回值语义：段未配置 → (nil, nil, nil) 表示跳过；构造失败 → error。
type ServerProvider func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error)

// Option 配置本 Provider 的行为（非 asynq 服务器的 Option——那是 asynq.Option）。
//
// 为什么需要它：asynq 的**任务处理器注册**（RegisterSubscriber）是应用逻辑，
// 不属于契约段的声明式配置。契约装配只负责构造 server，业务经 [WithHandlers]
// 在构造后、启动前挂上处理器。
type Option func(*providerConfig)

type providerConfig struct {
	handlers    []func(*asynq.Server) error
	extraOpts   []asynq.Option
	codecRegist func()
}

// WithHandlers 注册「server 构造后」的处理器挂载回调。
//
// **时序**：回调在 asynq.NewServer 之后、返回 server 之前执行——早于 appkit 的
// Start，满足 asynq「handler 须在 Start 前注册」的硬约束（启动后再注册会被判为
// 无处理器，任务入队后无消费者）。回调返回 error 则装配失败并短路。
func WithHandlers(fn func(*asynq.Server) error) Option {
	return func(c *providerConfig) { c.handlers = append(c.handlers, fn) }
}

// WithServerOptions 追加 asynq.Option——补齐契约段表达不了的能力（约 46 个
// Option 中的其余部分）。追加项在契约字段**之后**应用，故可覆盖契约值。
func WithServerOptions(opts ...asynq.Option) Option {
	return func(c *providerConfig) { c.extraOpts = append(c.extraOpts, opts...) }
}

// WithCodecRegistration 注册 codec 的初始化回调，在构造 server **之前**执行。
//
// 必要原因（实测约束）：asynq 的 WithCodec(name) 内部调 encoding.GetCodec(name)，
// 对未注册名**静默设 nil**——故障延迟到首次入队才以 "codec is nil" 暴露。故业务
// 须先把 codec 注册进 encoding 全局表（如 encoding.MustRegister(json.New())），
// 本回调提供该时机。
func WithCodecRegistration(fn func()) Option {
	return func(c *providerConfig) { c.codecRegist = fn }
}

// Provider 返回契约驱动的 asynq ServerProvider。
//
// 仅当契约 server.asynq 段存在时构造（段缺失返回 nil server，BuildServers 跳过）。
// 段字段按「未设置即用实现默认」语义映射（区别于契约零值——见 proto 中 optional
// 三态与 0 哨兵的说明）。
func Provider(opts ...Option) ServerProvider {
	c := &providerConfig{}
	for _, o := range opts {
		o(c)
	}
	return func(_ context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		sec := cfg.GetAsynq()
		if sec == nil {
			return nil, nil, nil // 未配置 asynq 段，跳过
		}

		if c.codecRegist != nil {
			c.codecRegist()
		}

		allOpts := buildOpts(sec)
		allOpts = append(allOpts, c.extraOpts...)

		srv := asynq.NewServer(allOpts...)

		for _, fn := range c.handlers {
			if err := fn(srv); err != nil {
				return nil, nil, fmt.Errorf("asynq: register handlers: %w", err)
			}
		}
		return srv, nil, nil
	}
}

// buildOpts 把契约 asynq 段映射为 asynq.Option 序列。
//
// 「未设置」语义：optional bool 为 nil 时**不**调对应 Option（用实现默认）；
// 数值 0 视为未设置（0 非合法配置值，作哨兵）。这避免了「空段覆盖实现默认」
// 的反向陷阱（如 scheduler 默认启用，若恒传 WithSchedulerEnabled(false) 会关掉它）。
func buildOpts(sec *bootstrapv1.Server_Asynq) []asynq.Option {
	var opts []asynq.Option

	if addr := sec.GetRedisAddress(); addr != "" {
		opts = append(opts, asynq.WithRedisAddress(addr))
	}
	if pwd := sec.GetRedisPassword(); pwd != "" {
		opts = append(opts, asynq.WithRedisPassword(pwd))
	}
	if db := sec.GetRedisDb(); db != 0 {
		opts = append(opts, asynq.WithRedisDB(db))
	}
	if n := sec.GetConcurrency(); n > 0 {
		opts = append(opts, asynq.WithConcurrency(n))
	}
	if qs := sec.GetQueues(); len(qs) > 0 {
		opts = append(opts, asynq.WithQueues(queueWeights(qs)))
	}
	if name := sec.GetCodec(); name != "" {
		opts = append(opts, asynq.WithCodec(name))
	}
	if v := sec.StrictPriority; v != nil {
		opts = append(opts, asynq.WithStrictPriority(*v))
	}
	if ms := sec.GetShutdownTimeoutMs(); ms > 0 {
		opts = append(opts, asynq.WithShutdownTimeout(time.Duration(ms)*time.Millisecond))
	}
	if v := sec.GracefullyShutdown; v != nil {
		opts = append(opts, asynq.WithGracefullyShutdown(*v))
	}
	if v := sec.SchedulerEnabled; v != nil {
		opts = append(opts, asynq.WithSchedulerEnabled(*v))
	}
	return opts
}

// queueWeights 把契约的「优先级从高到低队列名列表」映射为 asynq 的权重表。
//
// 契约是 ordered list（第 0 项优先级最高），实现收 map[string]int 权重。映射为
// 递减权重（首项 = len，末项 = 1）——保证相对优先级与契约声明一致。等权（全 1）
// 会让 asynq 退化为轮询，丢失契约表达的优先级信息。
func queueWeights(queues []string) map[string]int32 {
	w := make(map[string]int32, len(queues))
	for i, name := range queues {
		w[name] = int32(len(queues) - i)
	}
	return w
}

// 类型守卫：Provider 返回的签名与 bootstrap.ServerProvider 结构化兼容
// （不在本包 import bootstrap——避免 transport/asynq → bootstrap 的依赖）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
