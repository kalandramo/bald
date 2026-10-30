// Package contract 提供 SSE 传输层的契约装配：bootstrapv1.Server 的 sse 段 →
// sse.NewServer + 构造 func（供 server.sse 段 Provider 的 Build 复用）。
//
// 单独成包的原因（与 transport/{asynq,cron,gateway}/contract 同惯例）：
// transport/sse 保持零契约依赖（核心不 import bconf），只有本包 import
// bconf——业务按需 import，依赖图全程可见。
//
// 契约段表达传输参数（addr/path/tls）；授权钩子、订阅回调等业务接线经
// WithServerOptions / WithAfterBuild 注入，在 Provider 的 Build 内应用（时机
// 在业务 BeforeStart 之后，可读取业务运行期资源）。
//
// 与 bootstrap 的关系：本包不 import bootstrap（Provider 返回未命名函数类型，
// 用 var _ 守卫）。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport"
	"github.com/kalandramo/bald/transport/sse"
)

// Type 是契约 server 段中 sse 后端的段名。
const Type = "sse"

// Option 配置本 Provider 的行为（非 SSE 服务器的 Option——那是 sse.Option）。
type Option func(*providerConfig)

type providerConfig struct {
	// serverOpts 是「server 构造前」应用的 sse.Option（如授权钩子、路径）。
	serverOpts []sse.Option
	// afterBuild 是「server 构造后」执行的回调（如业务侧接线：把 server 适配为
	// 业务发布器）。返回 error 则装配失败并短路。
	afterBuild []func(context.Context, *sse.Server) error
}

// WithServerOptions 追加 sse.Option——覆盖契约段表达不了的能力。
//
// 这是与 sse 函数式接线（授权钩子 / 订阅回调 / 中间件 / 编解码器…）对接的主通道：
// transport/sse/options.go 的全部 Option 均可经此传入。追加项在契约字段**之后**
// 应用，故可覆盖契约值（如 WithPath 覆盖 sec.path）。
func WithServerOptions(opts ...sse.Option) Option {
	return func(c *providerConfig) { c.serverOpts = append(c.serverOpts, opts...) }
}

// WithAfterBuild 注册「server 构造后」的回调，用于业务侧接线。
//
// **时序**：回调在 sse.NewServer 之后、返回 server 之前执行——早于 appkit 的
// Start。典型用途是把构造出的 *sse.Server 注册为业务的推送发布器
// （如 bald-admin 的 message 域 Publisher 适配）。
//
// ctx 为 provider 收到的启动上下文（Run 期），可透传给回调内的日志/初始化。
func WithAfterBuild(fn func(context.Context, *sse.Server) error) Option {
	return func(c *providerConfig) { c.afterBuild = append(c.afterBuild, fn) }
}

// Provider 返回契约驱动的 SSE 工厂函数。
//
// 返回**未命名函数类型**——调用方在 server.sse 段 Provider 的 Build 中直接
// 调用，无需显式类型转换，本包也不必 import bootstrap。
//
// 仅当契约 server.sse 段存在时构造（段缺失返回 nil server，引擎跳过）。
//
// **addr 必填**：段存在但 addr 为空时 fail-fast——与 gateway contract 同判据
// （「配了段却不给监听地址」是漏配，显式优于隐式）。注意这与「用 addr 空串表示
// 不启用」的旧逃生舱语义相反：现在「不启用」的正确表达是**移除 sse 段**。
//
// 关于 tls：与 gateway contract 一致，本层只做契约字段到 sse.Option 的透传，
// **不**在此加载证书文件（框架内无通用 TLS 加载 helper，且各 transport 一律
// 只收 *tls.Config）。需要 TLS 时经 WithServerOptions(sse.WithTLSConfig(...))
// 从业务侧注入。
func Provider(opts ...Option) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	c := &providerConfig{}
	for _, o := range opts {
		o(c)
	}
	return func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		sec := cfg.GetSse()
		if sec == nil {
			return nil, nil, nil // 未配置 sse 段，跳过
		}
		if sec.GetAddr() == "" {
			return nil, nil, fmt.Errorf("sse: server.sse.addr is required (remove the sse section to disable)")
		}

		// 契约字段在前，业务 Option 在后（后者可覆盖前者——与 cron/asynq 同约定）。
		allOpts := buildOpts(sec)
		allOpts = append(allOpts, c.serverOpts...)

		srv := sse.NewServer(sec.GetAddr(), allOpts...)

		for _, fn := range c.afterBuild {
			if err := fn(ctx, srv); err != nil {
				return nil, nil, fmt.Errorf("sse: after-build hook: %w", err)
			}
		}
		return srv, nil, nil
	}
}

// buildOpts 把契约 sse 段映射为 sse.Option 序列。
//
// 「未设置」语义：path 为空时**不**调 WithPath——用实现默认值 "/events"
// （与契约注释 server.proto 的「默认 /events」一致）。这避免「空段 sse: {}
// 反而把 path 设成空串」的反向陷阱。
func buildOpts(sec *bootstrapv1.Server_Sse) []sse.Option {
	var opts []sse.Option
	if path := sec.GetPath(); path != "" {
		opts = append(opts, sse.WithPath(path))
	}
	return opts
}

// 类型守卫：Provider 保持构造 func 形状（供 server.sse 段 Provider 的 Build
// 调用；不在本包 import bootstrap——避免 transport/sse → bootstrap 的依赖）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
