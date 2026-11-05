// Package contract 提供 GraphQL 传输层的契约装配：bootstrapv1.Server 的
// graphql 段 → graphql.NewServer + 构造 func（供 server.graphql 段 Provider 的
// Build 复用）。
//
// 单独成包的原因（与 transport/{sse,cron,asynq,gateway,websocket}/contract 同惯例）：
// transport/graphql 保持零契约依赖（核心不 import bconf），只有本包 import
// bconf——业务按需 import，依赖图全程可见。
//
// 契约段表达传输参数（addr/tls）；schema 路由（Handle/HandleFunc）、中间件与
// TLS 覆盖经 WithServerOptions / WithAfterBuild 注入，在 Provider 的 Build 内应用
// （时机在业务 BeforeStart 之后、监听建立之前——graphql 的路由必须在 Start 前登记完）。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconf/tlsconf"

	"github.com/kalandramo/bald/transport"
	graphql "github.com/kalandramo/bald/transport/graphql"
)

// Type 是契约 server 段中 graphql 后端的段名。
const Type = "graphql"

// Option 配置本 Provider 的行为（非 GraphQL 服务器的 Option——那是 graphql.Option）。
type Option func(*providerConfig)

type providerConfig struct {
	// serverOpts 是「server 构造前」应用的 graphql.Option（如中间件、TLS 覆盖）。
	serverOpts []graphql.Option
	// afterBuild 是「server 构造后」执行的回调（如业务侧接线：注册 schema 路由）。
	// 返回 error 则装配失败并短路。
	afterBuild []func(context.Context, *graphql.Server) error
}

// WithServerOptions 追加 graphql.Option——覆盖契约段表达不了的能力。
//
// transport/graphql/options.go 的 WithTLSConfig / WithMiddleware 均可经此传入。
// 追加项在契约字段**之后**应用，故可覆盖契约值（含 TLS 证书）。
//
// **WithTLS 不在可用清单里**：它在证书读不出时 panic，而契约装配期 panic 会让
// 「配置写错」从启动错误变成进程崩溃。契约内建 TLS 一律走 bconf/tlsconf +
// WithTLSConfig；业务真要自取证书也应用 WithTLSConfig 而不是这个 panic 版。
func WithServerOptions(opts ...graphql.Option) Option {
	return func(c *providerConfig) { c.serverOpts = append(c.serverOpts, opts...) }
}

// WithAfterBuild 注册「server 构造后」的回调，用于业务侧接线。
//
// **时序**：回调在 graphql.NewServer 之后、返回 server 之前执行——早于 appkit 的
// Start，所以 Handle / HandleFunc / Use 必须在监听建立前登记完（graphql 的
// Start 会把 Start 时读到的 middlewares 定形，之后调用 Use 不生效）。
func WithAfterBuild(fn func(context.Context, *graphql.Server) error) Option {
	return func(c *providerConfig) { c.afterBuild = append(c.afterBuild, fn) }
}

// Provider 返回契约驱动的 GraphQL 工厂函数。
//
// 返回**未命名函数类型**——调用方在 server.graphql 段 Provider 的 Build 中直接
// 调用，无需显式类型转换，本包也不必 import bootstrap。
//
// 仅当契约 server.graphql 段存在时构造（段缺失返回 nil server，引擎跳过）。
//
// **addr 必填**：段存在但 addr 为空时 fail-fast——与 sse/tcp/websocket contract
// 同判据。graphql.NewServer 的 addr 是位置参数且直接进 net.Listen，空串会绑到
// 全部网卡的随机端口：「配置写了、服务起了、落点不由配置决定」。
//
// **tls 按契约加载**：段内 tls 非空即经 bconf/tlsconf 解析并交给
// graphql.WithTLSConfig（Endpoint 的方案随 tlsConfig 非 nil 切成 https）；
// tls 段缺席则是显式的明文 http。
func Provider(opts ...Option) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	c := &providerConfig{}
	for _, o := range opts {
		o(c)
	}
	return func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		sec := cfg.GetGraphql()
		if sec == nil {
			return nil, nil, nil // 未配置 graphql 段，跳过
		}
		if sec.GetAddr() == "" {
			return nil, nil, fmt.Errorf("graphql: server.graphql.addr is required (remove the graphql section to disable)")
		}

		// 契约字段在前，业务 Option 在后（后者可覆盖前者——与 sse/tcp/websocket 同约定）。
		base, err := buildOpts(sec)
		if err != nil {
			return nil, nil, err
		}
		allOpts := append(base, c.serverOpts...) //nolint:gocritic // base 是本函数新建的定长切片

		srv := graphql.NewServer(sec.GetAddr(), allOpts...)

		for _, fn := range c.afterBuild {
			if err := fn(ctx, srv); err != nil {
				return nil, nil, fmt.Errorf("graphql: after-build hook: %w", err)
			}
		}
		return srv, nil, nil
	}
}

// buildOpts 把契约 graphql 段映射为 graphql.Option 序列（addr 是位置参数，不在这里）。
//
// graphql 段只有 addr / tls 两个字段，所以这里只有 tls 一行；tls 按**段存在与否**
// 决定传不传：空 tls 段（`tls: {}`）是「启用但没证书」，交给 tlsconf 报错而不是
// 静默回落明文。
func buildOpts(sec *bootstrapv1.Server_Graphql) ([]graphql.Option, error) {
	var opts []graphql.Option
	if tlsSec := sec.GetTls(); tlsSec != nil {
		tc, err := tlsconf.Server(tlsSec)
		if err != nil {
			return nil, fmt.Errorf("graphql: server.graphql.tls: %w", err)
		}
		opts = append(opts, graphql.WithTLSConfig(tc))
	}
	return opts, nil
}

// 类型守卫：Provider 保持构造 func 形状（供 server.graphql 段 Provider 的
// Build 调用；不在本包 import bootstrap——避免该依赖）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
