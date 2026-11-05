// Package contract 提供 WebRTC 传输层的契约装配：bootstrapv1.Server 的
// webrtc 段 → webrtc.NewServer + 构造 func（供 server.webrtc 段 Provider 的
// Build 复用）。
//
// 单独成包的原因（与 transport/{sse,cron,asynq,gateway,websocket,graphql}/contract
// 同惯例）：transport/webrtc 保持零契约依赖（核心不 import bconf），只有本包
// import bconf——业务按需 import，依赖图全程可见。
//
// 契约段五个字段全部可映射（addr / network / tls / codec / path）；DataChannel
// 标签、CORS、Origin 校验、媒体开关、payload 类型等代码侧能力经
// WithServerOptions 注入，消息处理器经 WithAfterBuild 注入。
package contract

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/encoding"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconf/tlsconf"

	"github.com/kalandramo/bald/transport"
	webrtc "github.com/kalandramo/bald/transport/webrtc"
)

// Type 是契约 server 段中 webrtc 后端的段名。
const Type = "webrtc"

// Option 配置本 Provider 的行为（非 WebRTC 服务器的 Option——那是 webrtc.ServerOption）。
type Option func(*providerConfig)

type providerConfig struct {
	// serverOpts 是「server 构造前」应用的 webrtc.ServerOption。
	serverOpts []webrtc.ServerOption
	// afterBuild 是「server 构造后」执行的回调（如业务侧接线：注册消息处理器）。
	// 返回 error 则装配失败并短路。
	afterBuild []func(context.Context, *webrtc.Server) error
}

// WithServerOptions 追加 webrtc.ServerOption——覆盖契约段表达不了的能力。
//
// transport/webrtc/server_options.go 的全部 ServerOption 均可经此传入（媒体、
// DataChannel 标签、CORS、Origin、payloadType、注入自定义 listener 等）。
// 追加项在契约字段**之后**应用，故可覆盖契约值。
//
// 两点提醒：WithCodec 传未注册的名字会被底层静默回落到 json（本包对契约里的
// codec 直接报错，见 buildOpts），而 WithChannelBufferSize 写的是**包级变量**
// （同进程多个 server 会互相影响），不要用它在单实例上「调缓冲」。
func WithServerOptions(opts ...webrtc.ServerOption) Option {
	return func(c *providerConfig) { c.serverOpts = append(c.serverOpts, opts...) }
}

// WithAfterBuild 注册「server 构造后」的回调，用于业务侧接线。
//
// **时序**：回调在 webrtc.NewServer 之后、返回 server 之前执行——早于 appkit 的
// Start。信令路由（mux）在 NewServer 的 init 里定形，之后不能再加路径；
// 但消息处理器表由 handlerMu 保护，RegisterMessageHandler 在 Start 之后仍有效。
func WithAfterBuild(fn func(context.Context, *webrtc.Server) error) Option {
	return func(c *providerConfig) { c.afterBuild = append(c.afterBuild, fn) }
}

// Provider 返回契约驱动的 WebRTC 工厂函数。
//
// 返回**未命名函数类型**——调用方在 server.webrtc 段 Provider 的 Build 中直接
// 调用，无需显式类型转换，本包也不必 import bootstrap。
//
// 仅当契约 server.webrtc 段存在时构造（段缺失返回 nil server，引擎跳过）。
//
// **addr 必填**：段存在但 addr 为空时 fail-fast——与 sse/tcp/websocket contract
// 同判据。webrtc 的 address 默认 ":0"，但契约字段空串会覆盖成 ""，而
// net.Listen(network, "") 绑到全部网卡的随机端口：「配置写了、服务起了、落点
// 不由配置决定」。
//
// **NewServer 的 nil 返回必须挡住**：webrtc.NewServer 在 init 出错时返回 nil
// （只打一行日志），把这种形状直接当 transport.Server 会变成「非 nil 接口装 nil
// 指针」，故障点漂到 Start 的空指针 panic。本包构造后判 nil 即报错。
//
// **tls 按契约加载**：段内 tls 非空即经 bconf/tlsconf 解析并交给
// webrtc.WithTLSConfig（信令走 https，Endpoint 方案同步）；tls 段缺席是明文 http。
func Provider(opts ...Option) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	c := &providerConfig{}
	for _, o := range opts {
		o(c)
	}
	return func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		sec := cfg.GetWebrtc()
		if sec == nil {
			return nil, nil, nil // 未配置 webrtc 段，跳过
		}
		if sec.GetAddr() == "" {
			return nil, nil, fmt.Errorf("webrtc: server.webrtc.addr is required (remove the webrtc section to disable)")
		}

		// 契约字段在前，业务 Option 在后（后者可覆盖前者——与 sse/tcp/graphql 同约定）。
		base, err := buildOpts(sec)
		if err != nil {
			return nil, nil, err
		}
		allOpts := append(base, c.serverOpts...) //nolint:gocritic // base 是本函数新建的定长切片

		srv := webrtc.NewServer(allOpts...)
		if srv == nil {
			return nil, nil, fmt.Errorf("webrtc: server.webrtc: webrtc.NewServer returned nil (init failed; see the webrtc init log line)")
		}

		for _, fn := range c.afterBuild {
			if err := fn(ctx, srv); err != nil {
				return nil, nil, fmt.Errorf("webrtc: after-build hook: %w", err)
			}
		}
		return srv, nil, nil
	}
}

// buildOpts 把契约 webrtc 段映射为 webrtc.ServerOption 序列。
//
// 三条「未设置」语义：
//   - addr 是**唯一**必须非空的字段，所以在这里无条件写（其余字段空即不追加）；
//   - network / path 空 → 不追加，保留实现默认（tcp、/signal）——底层 WithNetwork /
//     WithPath 自己也跳过空串，但契约侧不追加更直白：默认值只有一个来源；
//   - codec 非空但**注册表里没有** → 直接报错。底层形状是 WithCodec 先
//     `s.codec = encoding.GetCodec(c)`（未注册即 nil），init 末尾再对 nil 兜底成
//     json——于是「契约写 proto」实际跑的是 json，一声不响。装配期注册表已定形
//     （codec 由包 init 或 main 显式注册），所以这里判得准，报错比兜底诚实。
//
// tls 按**段存在与否**决定传不传：空 tls 段（`tls: {}`）是「启用但没证书」，
// 交给 tlsconf 报错而不是静默回落明文。
func buildOpts(sec *bootstrapv1.Server_Webrtc) ([]webrtc.ServerOption, error) {
	opts := []webrtc.ServerOption{webrtc.WithAddress(sec.GetAddr())}

	if network := sec.GetNetwork(); network != "" {
		opts = append(opts, webrtc.WithNetwork(network))
	}
	if path := sec.GetPath(); path != "" {
		opts = append(opts, webrtc.WithPath(path))
	}
	if codec := sec.GetCodec(); codec != "" {
		if encoding.GetCodec(codec) == nil {
			return nil, fmt.Errorf("webrtc: server.webrtc.codec %q is not registered (import the codec package and register it, e.g. encoding.MustRegister(jsoncodec.New()), or drop the codec field)", codec)
		}
		opts = append(opts, webrtc.WithCodec(codec))
	}
	if tlsSec := sec.GetTls(); tlsSec != nil {
		tc, err := tlsconf.Server(tlsSec)
		if err != nil {
			return nil, fmt.Errorf("webrtc: server.webrtc.tls: %w", err)
		}
		opts = append(opts, webrtc.WithTLSConfig(tc))
	}
	return opts, nil
}

// 类型守卫：Provider 保持构造 func 形状（供 server.webrtc 段 Provider 的
// Build 调用；不在本包 import bootstrap——避免该依赖）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
