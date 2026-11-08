// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// recovery.go 是统一形态的 panic 兜底：一份实现经两座桥同时服务 gRPC 与
// HTTP（方案 S 批次 2 的改写样板——语义取 gRPC 侧现行口径，两侧旧链保留至
// 批次 3）。

package middleware

import (
	"context"

	"runtime/debug"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/log"
)

// Recovery 恢复统一链上 handler/内层中间件的 panic 并返回 Internal 错误：
// panic 记日志（含堆栈）后以 berrors.Internal("PANIC") 透出，由调用面的
// 错误收口层（gRPC 的 ErrorInterceptor / gin 的桥渲染）落成最终响应。
//
// 链序约定：放在错误收口层之后（第二外层）——panic 转成的 *berrors.Error
// 才能被外层收口；RequestID 等内层中间件的 panic 均被覆盖。
func Recovery() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, req interface{}) (resp interface{}, err error) {
			defer func() {
				if r := recover(); r != nil {
					operation := "unknown"
					if tr, ok := FromServerContext(ctx); ok {
						operation = tr.Operation()
					}
					log.Error(ctx, "handler panicked",
						"panic", r, "operation", operation, "stack", string(debug.Stack()))
					err = berrors.Internal("PANIC").WithMessage("internal panic: %v", r)
					resp = nil
				}
			}()
			return next(ctx, req)
		}
	}
}
