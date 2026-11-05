// Package contract 提供 MCP 传输层的契约装配：bootstrapv1.Server 的 mcp 段 →
// mcp.NewServer + 构造 func（供 server.mcp 段 Provider 的 Build 复用）。
//
// 单独成包的原因（与 transport/{sse,cron,asynq,gateway,websocket,graphql,webrtc}/
// contract 同惯例）：transport/mcp 保持零契约依赖（核心不 import bconf），只有本包
// import bconf——业务按需 import，依赖图全程可见。
//
// 契约段三个字段全部映射（name / version / address）；工具与资源注册、底层
// mcp-go 选项、HTTP 中间件、OAuth 资源元数据经 WithServerOptions / WithAfterBuild
// 注入。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport"
	mcp "github.com/kalandramo/bald/transport/mcp"
)

// Type 是契约 server 段中 mcp 后端的段名。
const Type = "mcp"

// Option 配置本 Provider 的行为（非 MCP 服务器的 Option——那是 mcp.ServerOption）。
type Option func(*providerConfig)

type providerConfig struct {
	// serverOpts 是「server 构造前」应用的 mcp.ServerOption。
	serverOpts []mcp.ServerOption
	// afterBuild 是「server 构造后」执行的回调（如业务侧接线：注册工具 / 资源）。
	// 返回 error 则装配失败并短路。
	afterBuild []func(context.Context, *mcp.Server) error
}

// WithServerOptions 追加 mcp.ServerOption——覆盖契约段表达不了的能力。
//
// transport/mcp/options.go 的全部 ServerOption 均可经此传入（底层 mcp-go 选项、
// SSE 选项、HTTP 中间件、RFC 9728 资源元数据、传输形态 WithMCPServeType）。
// 追加项在契约字段**之后**应用，故可覆盖契约值——包括把形态从 HTTP 改回 STDIO，
// 但那时 address 就不再决定落点，属于业务自担的语义变化。
func WithServerOptions(opts ...mcp.ServerOption) Option {
	return func(c *providerConfig) { c.serverOpts = append(c.serverOpts, opts...) }
}

// WithAfterBuild 注册「server 构造后」的回调，用于业务侧接线。
//
// **时序**：回调在 mcp.NewServer 之后、返回 server 之前执行。工具/资源注册走的是
// 底层 *server.MCPServer 的 AddTool/AddResource，mcp-go 侧允许运行期追加，但
// 「先配好再听」是这里的口径：客户端 initialize 时看到的能力集应当是装配完的。
func WithAfterBuild(fn func(context.Context, *mcp.Server) error) Option {
	return func(c *providerConfig) { c.afterBuild = append(c.afterBuild, fn) }
}

// Provider 返回契约驱动的 MCP 工厂函数。
//
// 返回**未命名函数类型**——调用方在 server.mcp 段 Provider 的 Build 中直接调用，
// 无需显式类型转换，本包也不必 import bootstrap。
//
// 仅当契约 server.mcp 段存在时构造（段缺失返回 nil server，引擎跳过）。
//
// **address 必填**：段存在但 address 为空时 fail-fast——与 sse/tcp/websocket
// contract 同判据。不写 address 时实现默认 ":8080"，于是「配置写了、服务起了、
// 落点不由配置决定」，且 8080 是否与别的监听冲突由环境决定，不由配置决定。
//
// **形态固定为 HTTP（streamable）**：契约 mcp 段没有 type 字段，而实现的默认是
// STDIO——STDIO **不绑任何端口**，address 会被完全忽略。写了 address 就必须在
// 那个地址上听得见；要 STDIO 就别开这个段（或经 WithServerOptions 显式改回，
// 见上）。选 HTTP 而非 SSE 的依据是仓内既有姿势：transport/mcp 自身的生命周期
// 与协议回归用例都用 ServerTypeHTTP。
func Provider(opts ...Option) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	c := &providerConfig{}
	for _, o := range opts {
		o(c)
	}
	return func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		sec := cfg.GetMcp()
		if sec == nil {
			return nil, nil, nil // 未配置 mcp 段，跳过
		}
		if sec.GetAddress() == "" {
			return nil, nil, fmt.Errorf("mcp: server.mcp.address is required (remove the mcp section to disable)")
		}

		// 契约字段在前，业务 Option 在后（后者可覆盖前者——与 sse/tcp/websocket 同约定）。
		base := buildOpts(sec)
		allOpts := append(base, c.serverOpts...) //nolint:gocritic // base 是本函数新建的定长切片

		srv := mcp.NewServer(allOpts...)

		for _, fn := range c.afterBuild {
			if err := fn(ctx, srv); err != nil {
				return nil, nil, fmt.Errorf("mcp: after-build hook: %w", err)
			}
		}
		return srv, nil, nil
	}
}

// buildOpts 把契约 mcp 段映射为 mcp.ServerOption 序列。
//
// 三条「未设置」语义：
//   - address 是唯一必填字段，无条件写；
//   - 形态无条件写 HTTP（契约无 type 字段，见 Provider 的说明）；
//   - name / version 为空 → 不追加，保留实现默认（"MCP Server" / "1.0.0"）——
//     空串不是「把名字设空」，MCP 的 initialize 响应里 serverInfo.name 是客户端
//     展示用的必填字段。
//
// 契约 mcp 段没有 tls 字段，底层也没有 TLS 选项（两者一致，不是缺口）：要在契约
// 里启用 https 得先给 mcp-go 的 HTTP 层加证书通道，属能力面而非映射面。
func buildOpts(sec *bootstrapv1.Server_Mcp) []mcp.ServerOption {
	opts := []mcp.ServerOption{
		mcp.WithMCPServeAddress(sec.GetAddress()),
		mcp.WithMCPServeType(mcp.ServerTypeHTTP),
	}
	if name := sec.GetName(); name != "" {
		opts = append(opts, mcp.WithServerName(name))
	}
	if version := sec.GetVersion(); version != "" {
		opts = append(opts, mcp.WithServerVersion(version))
	}
	return opts
}

// 类型守卫：Provider 保持构造 func 形状（供 server.mcp 段 Provider 的 Build
// 调用；不在本包 import bootstrap——避免该依赖）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
