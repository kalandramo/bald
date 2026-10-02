// Package contract 提供 cron 定时任务调度器的契约装配：bootstrapv1.Server 的
// cron 段 → cron.Option 映射 + 契约驱动的 ServerProvider。
//
// 单独成包的原因：transport/cron 保持零契约依赖（纯调度器封装），只有本包
// import bconf——业务按需 import，依赖图全程可见。与 cache/redis/contract、
// transport/asynq/contract 同一模式。
//
// 与 bootstrap 的关系：本包**不** import bootstrap。Provider 的返回类型是
// 结构化兼容的自有函数类型，注册时反正地传入 bootstrap.ServerRegistry：
//
//	sr := baldbootstrap.NewServerRegistry()
//	sr.MustRegister(croncontract.Type, croncontract.Provider(...))
package contract

import (
	"context"
	"fmt"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport"
	"github.com/kalandramo/bald/transport/cron"
)

// Type 是契约 server 段中 cron 后端的段名。
const Type = "cron"

// ServerProvider 是契约驱动的服务器工厂签名（结构化兼容 bootstrap.ServerProvider）。
type ServerProvider func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error)

// Option 配置本 Provider 的行为（非 cron 服务器的 Option——那是 cron.Option）。
type Option func(*providerConfig)

type providerConfig struct {
	jobs      []func(*cron.Server) error
	extraOpts []cron.Option
}

// WithJobs 注册「server 构造后」的周期任务挂载回调。
//
// **时序**：回调在 cron.NewServer 之后、返回 server 之前执行——早于 appkit 的
// Start。cron 的 NewTimerJob 在 Start 前注册即可（Start 后也能加，但启动瞬间
// 的调度窗口会漏掉，故装配期注册更稳妥）。回调返回 error 则装配失败并短路。
func WithJobs(fn func(*cron.Server) error) Option {
	return func(c *providerConfig) { c.jobs = append(c.jobs, fn) }
}

// WithServerOptions 追加 cron.Option（补齐契约段表达不了的能力）。追加项在
// 契约字段**之后**应用，故可覆盖契约值。
func WithServerOptions(opts ...cron.Option) Option {
	return func(c *providerConfig) { c.extraOpts = append(c.extraOpts, opts...) }
}

// Provider 返回契约驱动的 cron ServerProvider。
//
// 仅当契约 server.cron 段存在时构造（段缺失返回 nil server，BuildServers 跳过）。
// 段字段按「未设置即用实现默认」语义映射（optional bool 为 nil 时不传 Option；
// location 为空不传时区）。
//
// 注意 cron 无需外部依赖（进程内调度器），故段存在时总是构造成功——与 asynq
// 不同（asynq 需 Redis，无地址时业务可自行降级）。
func Provider(opts ...Option) ServerProvider {
	c := &providerConfig{}
	for _, o := range opts {
		o(c)
	}
	return func(_ context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		sec := cfg.GetCron()
		if sec == nil {
			return nil, nil, nil // 未配置 cron 段，跳过
		}

		allOpts, err := buildOpts(sec)
		if err != nil {
			return nil, nil, err
		}
		allOpts = append(allOpts, c.extraOpts...)

		srv := cron.NewServer(allOpts...)

		for _, fn := range c.jobs {
			if err := fn(srv); err != nil {
				return nil, nil, fmt.Errorf("cron: register jobs: %w", err)
			}
		}
		return srv, nil, nil
	}
}

// buildOpts 把契约 cron 段映射为 cron.Option 序列。
//
// 「未设置」语义：optional bool 为 nil 时**不**调对应 Option（用实现默认——
// cron 的 seconds / gracefullyShutdown 默认均启用）。这避免了「空段 cron: {}
// 关掉秒级」的反向陷阱（D11 镜像：配了反而反向生效）。
//
// 而 location 解析失败视为**配置错误**（fail-fast）：时区名拼错会让任务在
// 意外时间触发，静默降级比报错危险。
func buildOpts(sec *bootstrapv1.Server_Cron) ([]cron.Option, error) {
	var opts []cron.Option

	if v := sec.Seconds; v != nil {
		opts = append(opts, cron.WithSeconds(*v))
	}
	if v := sec.GracefullyShutdown; v != nil {
		opts = append(opts, cron.WithGracefullyShutdown(*v))
	}
	if name := sec.GetLocation(); name != "" {
		loc, err := time.LoadLocation(name)
		if err != nil {
			return nil, fmt.Errorf("cron: invalid location %q: %w", name, err)
		}
		opts = append(opts, cron.WithLocation(loc))
	}
	return opts, nil
}

// 类型守卫：Provider 返回的签名与 bootstrap.ServerProvider 结构化兼容
// （不在本包 import bootstrap——避免 transport/cron → bootstrap 的依赖）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
