// Package contract 提供 WebSocket 传输层的契约装配：bootstrapv1.Server 的
// websocket 段 → websocket.NewServer + 构造 func（供 server.websocket 段
// Provider 的 Build 复用）。
//
// 单独成包的原因（与 transport/{sse,cron,asynq,gateway}/contract 同惯例）：
// transport/websocket 保持零契约依赖（核心不 import bconf），只有本包 import
// bconf——业务按需 import，依赖图全程可见。
//
// 契约段表达传输参数（addr/path/tls）；编解码器、会话回调、中间件、Origin 校验
// 等业务接线经 WithServerOptions / WithAfterBuild 注入，在 Provider 的 Build 内
// 应用（时机在业务 BeforeStart 之后，可读取业务运行期资源）。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconf/tlsconf"

	"github.com/kalandramo/bald/transport"
	websocket "github.com/kalandramo/bald/transport/websocket"
)

// Type 是契约 server 段中 websocket 后端的段名。
const Type = "websocket"

// Option 配置本 Provider 的行为（非 WS 服务器的 Option——那是 websocket.Option）。
type Option func(*providerConfig)

type providerConfig struct {
	// serverOpts 是「server 构造前」应用的 websocket.Option（如中间件、编解码器）。
	serverOpts []websocket.Option
	// afterBuild 是「server 构造后」执行的回调（如业务侧接线：注册消息处理器）。
	// 返回 error 则装配失败并短路。
	afterBuild []func(context.Context, *websocket.Server) error
}

// WithServerOptions 追加 websocket.Option——覆盖契约段表达不了的能力。
//
// 这是与 websocket 函数式接线（中间件 / 编解码器 / Origin 校验 / 缓冲区 /
// 握手超时 / 会话回调）对接的主通道：transport/websocket/options.go 的全部
// Option 均可经此传入。追加项在契约字段**之后**应用，故可覆盖契约值。
func WithServerOptions(opts ...websocket.Option) Option {
	return func(c *providerConfig) { c.serverOpts = append(c.serverOpts, opts...) }
}

// WithAfterBuild 注册「server 构造后」的回调，用于业务侧接线。
//
// **时序**：回调在 websocket.NewServer 之后、返回 server 之前执行——早于 appkit
// 的 Start，所以消息处理器必须在监听建立前登记完。
func WithAfterBuild(fn func(context.Context, *websocket.Server) error) Option {
	return func(c *providerConfig) { c.afterBuild = append(c.afterBuild, fn) }
}

// Provider 返回契约驱动的 WebSocket 工厂函数。
//
// 返回**未命名函数类型**——调用方在 server.websocket 段 Provider 的 Build 中
// 直接调用，无需显式类型转换，本包也不必 import bootstrap。
//
// 仅当契约 server.websocket 段存在时构造（段缺失返回 nil server，引擎跳过）。
//
// **addr 必填**：段存在但 addr 为空时 fail-fast——与 sse/tcp contract 同判据。
// 空串在这里不是「不监听」：net.Listen("tcp", "") 会绑到全部网卡的随机端口，
// 于是「配置写了、服务起了、落点不由配置决定」，端点等待与告警都指向一个没人
// 预期的地址。
//
// **tls 按契约加载**：段内 tls 非空即经 bconf/tlsconf 解析并交给
// websocket.WithTLSConfig（同时把 Endpoint 的方案切成 wss）。tls 段缺席则是
// 显式的明文 ws。
func Provider(opts ...Option) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	c := &providerConfig{}
	for _, o := range opts {
		o(c)
	}
	return func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		sec := cfg.GetWebsocket()
		if sec == nil {
			return nil, nil, nil // 未配置 websocket 段，跳过
		}
		if sec.GetAddr() == "" {
			return nil, nil, fmt.Errorf("websocket: server.websocket.addr is required (remove the websocket section to disable)")
		}

		// 契约字段在前，业务 Option 在后（后者可覆盖前者——与 sse/tcp 同约定）。
		base, err := buildOpts(sec)
		if err != nil {
			return nil, nil, err
		}
		allOpts := append(base, c.serverOpts...) //nolint:gocritic // base 是本函数新建的定长切片

		srv := websocket.NewServer(sec.GetAddr(), allOpts...)

		for _, fn := range c.afterBuild {
			if err := fn(ctx, srv); err != nil {
				return nil, nil, fmt.Errorf("websocket: after-build hook: %w", err)
			}
		}
		return srv, nil, nil
	}
}

// buildOpts 把契约 websocket 段映射为 websocket.Option 序列（addr 是位置参数，
// 不在这里）。
//
// 「未设置」语义：path 为空时**不**调 WithPath——用实现默认值 "/ws"（与契约注释
// server.proto 的「默认 /ws」一致），避免「空段 websocket: {} 反而把 path 设成
// 空串」的反向陷阱。tls 则按**段存在与否**决定传不传：空 tls 段（`tls: {}`）是
// 「启用但没证书」，交给 tlsconf 报错而不是静默回落明文。
func buildOpts(sec *bootstrapv1.Server_Websocket) ([]websocket.Option, error) {
	var opts []websocket.Option
	if path := sec.GetPath(); path != "" {
		opts = append(opts, websocket.WithPath(path))
	}
	if tlsSec := sec.GetTls(); tlsSec != nil {
		tc, err := tlsconf.Server(tlsSec)
		if err != nil {
			return nil, fmt.Errorf("websocket: server.websocket.tls: %w", err)
		}
		opts = append(opts, websocket.WithTLSConfig(tc))
	}
	return opts, nil
}

// 类型守卫：Provider 保持构造 func 形状（供 server.websocket 段 Provider 的
// Build 调用；不在本包 import bootstrap——避免该依赖）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
