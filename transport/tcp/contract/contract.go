// Package contract 提供 TCP 传输层的契约装配：bootstrapv1.Server 的 tcp 段 →
// tcp.NewServer + 构造 func（供 server.tcp 段 Provider 的 Build 复用）。
//
// 单独成包的原因（与 transport/{sse,cron,asynq,gateway}/contract 同惯例）：
// transport/tcp 保持零契约依赖（核心不 import bconf），只有本包 import
// bconf——业务按需 import，依赖图全程可见。
//
// 契约段表达传输参数（addr/tls）；编解码器、会话回调、报文装拆等业务接线经
// WithServerOptions / WithAfterBuild 注入，在 Provider 的 Build 内应用（时机
// 在业务 BeforeStart 之后，可读取业务运行期资源）。
package contract

import (
	"context"
	"encoding/binary"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconf/tlsconf"

	"github.com/kalandramo/bald/transport"
	"github.com/kalandramo/bald/transport/tcp"
)

// Type 是契约 server 段中 tcp 后端的段名。
const Type = "tcp"

// Option 配置本 Provider 的行为（非 TCP 服务器的 Option——那是 tcp.Option）。
type Option func(*providerConfig)

type providerConfig struct {
	// serverOpts 是「server 构造前」应用的 tcp.Option（如编解码器、报文装拆）。
	serverOpts []tcp.Option
	// afterBuild 是「server 构造后」执行的回调（如业务侧接线：注册消息处理器）。
	// 返回 error 则装配失败并短路。
	afterBuild []func(context.Context, *tcp.Server) error
}

// WithServerOptions 追加 tcp.Option——覆盖契约段表达不了的能力。
//
// 这是与 tcp 函数式接线（编解码器 / 会话回调 / 报文装拆 / tracer / metrics）
// 对接的主通道：transport/tcp/server_options.go 的全部 Option 均可经此传入。
// 追加项在契约字段**之后**应用，故可覆盖契约值。
func WithServerOptions(opts ...tcp.Option) Option {
	return func(c *providerConfig) { c.serverOpts = append(c.serverOpts, opts...) }
}

// WithAfterBuild 注册「server 构造后」的回调，用于业务侧接线。
//
// **时序**：回调在 tcp.NewServer 之后、返回 server 之前执行——早于 appkit 的
// Start，所以消息处理器必须在监听建立前登记完。
func WithAfterBuild(fn func(context.Context, *tcp.Server) error) Option {
	return func(c *providerConfig) { c.afterBuild = append(c.afterBuild, fn) }
}

// Provider 返回契约驱动的 TCP 工厂函数。
//
// 返回**未命名函数类型**——调用方在 server.tcp 段 Provider 的 Build 中直接
// 调用，无需显式类型转换，本包也不必 import bootstrap。
//
// 仅当契约 server.tcp 段存在时构造（段缺失返回 nil server，引擎跳过）。
//
// **addr 必填**：段存在但 addr 为空时 fail-fast（与 sse/gateway contract 同判据）。
// 注意 tcp.NewServer 不给地址时会落到 ":0" 随机端口——那是一条「配置写了却没按
// 预期落点监听」的静默失效路径，所以这里不接受空值。
//
// **tls 按契约加载**：段内 tls 非空即经 bconf/tlsconf 解析证书并交给
// tcp.WithTLSConfig——与内置的 http/grpc 服务器同一姿势（这两者在 transport 层
// 直接读 tls 段）。tlsconf 对「有 tls 段却无证书/私钥」自行 fail-fast，本层不
// 重复校验；tls 段缺席则不启用 TLS（明文监听是显式选择，不是漏配的默认）。
func Provider(opts ...Option) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	c := &providerConfig{}
	for _, o := range opts {
		o(c)
	}
	return func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		sec := cfg.GetTcp()
		if sec == nil {
			return nil, nil, nil // 未配置 tcp 段，跳过
		}
		if sec.GetAddr() == "" {
			return nil, nil, fmt.Errorf("tcp: server.tcp.addr is required (remove the tcp section to disable)")
		}

		// 契约字段在前，业务 Option 在后（后者可覆盖前者——与 sse/cron/asynq 同约定）。
		base, err := buildOpts(sec)
		if err != nil {
			return nil, nil, err
		}
		allOpts := append(base, c.serverOpts...) //nolint:gocritic // base 是本函数新建的定长切片

		srv := tcp.NewServer(allOpts...)

		for _, fn := range c.afterBuild {
			if err := fn(ctx, srv); err != nil {
				return nil, nil, fmt.Errorf("tcp: after-build hook: %w", err)
			}
		}
		return srv, nil, nil
	}
}

// buildOpts 把契约 tcp 段映射为 tcp.Option 序列。
//
// addr 无条件传（工厂入口已挡空值）；tls 按**段存在与否**决定传不传——空 tls
// 段（`tls: {}`）不是「不启用 TLS」而是「启用但没证书」，交给 tlsconf 报错，
// 而不是静默回落明文。
//
// byte_order 未指定时不传 Option（实例字段留零值 → 落到 tcp 包的默认小端）；
// 显式值走 tcp.WithByteOrder——只影响本实例，不动包级默认。
func buildOpts(sec *bootstrapv1.Server_Tcp) ([]tcp.Option, error) {
	opts := []tcp.Option{tcp.WithAddress(sec.GetAddr())}
	if tlsSec := sec.GetTls(); tlsSec != nil {
		tc, err := tlsconf.Server(tlsSec)
		if err != nil {
			return nil, fmt.Errorf("tcp: server.tcp.tls: %w", err)
		}
		opts = append(opts, tcp.WithTLSConfig(tc))
	}
	switch boom := sec.GetByteOrder(); boom {
	case bootstrapv1.Server_Tcp_BYTE_ORDER_UNSPECIFIED:
		// 沿用实例默认（小端），不传 Option。
	case bootstrapv1.Server_Tcp_LITTLE:
		opts = append(opts, tcp.WithByteOrder(binary.LittleEndian))
	case bootstrapv1.Server_Tcp_BIG:
		opts = append(opts, tcp.WithByteOrder(binary.BigEndian))
	default:
		// 枚举值来自契约文件，出现未知值说明 proto 与实现不同步；静默回落
		// 默认字节序会让报文头静默反解，故直接判配置错。
		return nil, fmt.Errorf("tcp: server.tcp.byte_order = %d is unknown", int32(boom))
	}
	return opts, nil
}

// 类型守卫：Provider 保持构造 func 形状（供 server.tcp 段 Provider 的 Build
// 调用；不在本包 import bootstrap——避免 transport/tcp → bootstrap 的依赖）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
