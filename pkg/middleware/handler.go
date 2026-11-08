// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package middleware

import "context"

// Handler 是统一的请求处理签名。它与 grpc.UnaryHandler 完全同形——这正是
// gRPC 桥近乎零成本的原因。一份横切关注（recovery/logging/tracing/timeout）
// 写成 Middleware 后经三座桥同时服务 gRPC 与 HTTP（自 onexmesh 移植的核心
// 契约；本仓方案的统一面，见 docs/architecture/transport-unification-assessment.md）。
type Handler func(ctx context.Context, req interface{}) (interface{}, error)

// Middleware 装饰一个 Handler（装饰器 + 责任链模式）。
type Middleware func(Handler) Handler

// Chain 组装中间件：第一个参数最外层（最先执行），最后一个最内层（最贴近
// 业务 handler）。
//
//	Chain(m1, m2, m3)(h) == m1(m2(m3(h)))
func Chain(outer Middleware, others ...Middleware) Middleware {
	// 从最内层（others 末位）向外组合。
	m := passthrough
	for i := len(others) - 1; i >= 0; i-- {
		inner, mw := m, others[i]
		m = func(h Handler) Handler { return mw(inner(h)) }
	}
	outerWrap := outer
	return func(h Handler) Handler { return outerWrap(m(h)) }
}

// passthrough 是恒等 Middleware：不加任何东西。导出别名 Passthrough 供装配
// 期「占位链」使用。
var passthrough Middleware = func(next Handler) Handler { return next }

// Passthrough 返回恒等 Middleware 的新实例（不加任何东西）。
func Passthrough() Middleware { return func(next Handler) Handler { return next } }
