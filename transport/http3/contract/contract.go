// Package contract 提供 HTTP/3 传输层的契约装配：bootstrapv1.Server 的 http3
// 段 → http3.NewServer + 构造 func（供 server.http3 段 Provider 的 Build 复用）。
//
// 单独成包的原因（与 transport/{sse,cron,asynq,gateway}/contract 同惯例）：
// transport/http3 保持零契约依赖（核心不 import bconf），只有本包 import
// bconf——业务按需 import，依赖图全程可见。
//
// 契约段表达传输参数（addr/tls）；路由、中间件、编解码等业务接线经
// WithServerOptions / WithAfterBuild 注入，在 Provider 的 Build 内应用（时机
// 在业务 BeforeStart 之后，可读取业务运行期资源）。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconf/tlsconf"

	"github.com/kalandramo/bald/transport"
	"github.com/kalandramo/bald/transport/http3"
)

// Type 是契约 server 段中 http3 后端的段名。
const Type = "http3"

// Option 配置本 Provider 的行为（非 HTTP/3 服务器的 Option——那是 http3.ServerOption）。
type Option func(*providerConfig)

type providerConfig struct {
	// serverOpts 是「server 构造前」应用的 http3.ServerOption（如中间件、编解码）。
	serverOpts []http3.ServerOption
	// afterBuild 是「server 构造后」执行的回调（如业务侧接线：挂路由）。
	// 返回 error 则装配失败并短路。
	afterBuild []func(context.Context, *http3.Server) error
}

// WithServerOptions 追加 http3.ServerOption——覆盖契约段表达不了的能力。
//
// 这是与 http3 函数式接线（中间件 / 过滤器 / 编解码 / 严格斜杠）对接的主通道：
// transport/http3/options.go 的全部 ServerOption 均可经此传入。追加项在契约字段
// **之后**应用，故可覆盖契约值。
func WithServerOptions(opts ...http3.ServerOption) Option {
	return func(c *providerConfig) { c.serverOpts = append(c.serverOpts, opts...) }
}

// WithAfterBuild 注册「server 构造后」的回调，用于业务侧接线（如
// `srv.Route("/x").GET(...)`——路由必须在 Start 前登记）。
func WithAfterBuild(fn func(context.Context, *http3.Server) error) Option {
	return func(c *providerConfig) { c.afterBuild = append(c.afterBuild, fn) }
}

// Provider 返回契约驱动的 HTTP/3 工厂函数。
//
// 返回**未命名函数类型**——调用方在 server.http3 段 Provider 的 Build 中直接
// 调用，无需显式类型转换，本包也不必 import bootstrap。
//
// 仅当契约 server.http3 段存在时构造（段缺失返回 nil server，引擎跳过）。
//
// **addr 必填**：段存在但 addr 为空时 fail-fast。http3 的默认落点是 ":443"
// （init 里写死），所以「配了段却不给地址」不会报错，而是**静默去抢 443**——
// 那是一条比错端口更难查的失效，故不接受空值。
//
// **tls 按契约加载**：段内 tls 非空即经 bconf/tlsconf 解析并交给
// http3.WithTLSConfig。**tls 段缺席时不传该 Option**，让实现自己生成自签证书
// （HTTP/3 必须有 TLS 上下文，这是 QUIC 的协议要求而非配置漏洞）——所以
// 「不配 tls」的真实后果是「用自签证书上线」，与契约里那句「默认 /events」式的
// 可回落默认不同，这里没有「关掉 TLS」这个选项。
func Provider(opts ...Option) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	c := &providerConfig{}
	for _, o := range opts {
		o(c)
	}
	return func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		sec := cfg.GetHttp3()
		if sec == nil {
			return nil, nil, nil // 未配置 http3 段，跳过
		}
		if sec.GetAddr() == "" {
			return nil, nil, fmt.Errorf("http3: server.http3.addr is required (remove the http3 section to disable; the implementation default is :443)")
		}

		// 契约字段在前，业务 Option 在后（后者可覆盖前者——与 sse/tcp 同约定）。
		base, err := buildOpts(sec)
		if err != nil {
			return nil, nil, err
		}
		allOpts := append(base, c.serverOpts...) //nolint:gocritic // base 是本函数新建的定长切片

		srv := http3.NewServer(allOpts...)

		for _, fn := range c.afterBuild {
			if err := fn(ctx, srv); err != nil {
				return nil, nil, fmt.Errorf("http3: after-build hook: %w", err)
			}
		}
		return srv, nil, nil
	}
}

// buildOpts 把契约 http3 段映射为 http3.ServerOption 序列。
//
// addr 无条件传（工厂入口已挡空值）；tls 按**段存在与否**决定传不传——空 tls 段
// 交给 tlsconf 报错，段缺席则留给实现的自签默认。
func buildOpts(sec *bootstrapv1.Server_Http3) ([]http3.ServerOption, error) {
	opts := []http3.ServerOption{http3.WithAddress(sec.GetAddr())}
	if tlsSec := sec.GetTls(); tlsSec != nil {
		tc, err := tlsconf.Server(tlsSec)
		if err != nil {
			return nil, fmt.Errorf("http3: server.http3.tls: %w", err)
		}
		opts = append(opts, http3.WithTLSConfig(tc))
	}
	return opts, nil
}

// 类型守卫：Provider 保持构造 func 形状（供 server.http3 段 Provider 的 Build
// 调用；不在本包 import bootstrap——避免该依赖）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
