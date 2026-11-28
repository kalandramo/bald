// Package contract 提供 HTTP 协议服务器的契约装配：bootstrapv1.Server 的 http 段
// → transport/http 构造参数映射 + 业务 handler 注入（供 server.http 段 Provider
// 的 Build 复用）。
//
// 为什么在本包而不是 bootstrap：其余能力面的「契约段 → 具体实现」知识一律住在
// `X/contract`，唯独 http/grpc 两面曾把工厂放在 bootstrap kernel（评审 N-1，
// docs/architecture/architecture-review-20261128.md）——kernel 因此反向 import
// 两个具体传输实现，同一个角色两套装配形状。本包把两面并回单一模式：
// bootstrap 不再认识 transport/http。
//
// 与 transport/http 的关系：实现根包保持零契约依赖（纯 net/http 封装），只有
// 本包 import bconf——与 transport/cron、transport/gateway 同一模式。
//
// 与装配层的关系：本包既不 import pkg/appkit 也不 import bootstrap。Provider
// 返回**未命名函数类型**，调用方直接调用取回 transport.Server（调用者是 appkit
// 的内置 server.http 段 Provider，见 appkit.StandardHTTPProvider；手工装配的
// 应用代码同样可直接用）。健康探针不在本包：探针包装属装配层职责，协议实现
// 不注册任何框架路由（见《Bald 健康检查装配设计》）。
package contract

import (
	"context"
	"net/http"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport"
	httpserver "github.com/kalandramo/bald/transport/http"
)

// Option 配置本 Provider 的业务依赖（不是 HTTP 服务器的 Option——那是
// httpserver 的构造参数；本包的职责是「契约段 + 业务依赖 → 服务器」）。
type Option func(*providerConfig)

type providerConfig struct {
	handler http.Handler
}

// WithHandler 注入业务 HTTP handler（gin.Engine 等，直接作为根 handler）。
//
// 探针（/healthz、/readyz）由业务 handler 自己拥有，或由装配层在其外层包装
// （appkit.WithHealth）——协议实现不注册任何框架路由。
func WithHandler(h http.Handler) Option {
	return func(c *providerConfig) { c.handler = h }
}

// Provider 返回契约驱动的 HTTP 服务器工厂（契约 Server.Http 段）。
//
// 返回值语义：出错短路；nil server 表示契约未配置 http 段（引擎跳过该产物）；
// cleanup 释放资源，可为 nil。
//
// 边界：grpc-gateway 转码面不在这里——本包不看 `server.http.driver`，同端口
// 单面的转码拓扑由 transport/gateway/contract 的 HTTPProvider 接管 server.http
// 段表达（段路径末者胜，见《Bald 传输层设计》）。
func Provider(opts ...Option) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	c := &providerConfig{}
	for _, o := range opts {
		o(c)
	}
	return func(_ context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		sec := cfg.GetHttp()
		if sec == nil {
			return nil, nil, nil // 未配置 http，跳过
		}
		return httpserver.NewHTTPServer(sec, c.handler), nil, nil
	}
}

// 类型守卫：Provider 保持构造 func 形状（调用方在 server.http 段 Provider 的
// Build 里直接调用，本包不必 import bootstrap/appkit 的任何类型）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
