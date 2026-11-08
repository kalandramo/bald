// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package matcher

import (
	"context"

	"github.com/kalandramo/bald/pkg/middleware"
)

// Match 返回一个统一 Middleware：每请求按当前 transport operation（gRPC 完整
// 方法名或 "METHOD /路由模板"）选出中间件并链在 next 周围。它与
// middleware.UnaryServerInterceptor / GinHandler 两座桥组合——一个 matcher
// 同时服务双协议。
func Match(m *Matcher) middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			tr, ok := middleware.FromServerContext(ctx)
			if !ok {
				return next(ctx, req)
			}
			mws := m.Match(tr.Operation())
			if len(mws) == 0 {
				return next(ctx, req)
			}
			return middleware.Chain(mws[0], mws[1:]...)(next)(ctx, req)
		}
	}
}
