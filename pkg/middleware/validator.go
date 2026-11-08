// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// validator.go 是统一形态的请求默认值填充与校验（自 gRPC 链合并；方案 S 批次 2）。

package middleware

import (
	"context"
)

// RequestDefaulter 定义可被统一链调用的请求默认值填充接口。
type RequestDefaulter interface {
	Default()
}

// Defaulter 在 handler 之前对请求执行默认值填充；请求未实现接口时直通。
func Defaulter() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			if defaulter, ok := req.(RequestDefaulter); ok {
				defaulter.Default()
			}
			return next(ctx, req)
		}
	}
}

// MessageValidator 校验一个请求对象，返回 error 表示校验不通过。
//
// 本层刻意**不绑定任何具体校验库**（核心只依赖函数类型，实现由调用方注入）：
//   - 声明式字段规则（proto 的 buf.validate 注解）用 protovalidate；
//   - 复杂命令式逻辑（查库、权限比对）用 pkg/validation 的分发器或手写函数；
//   - 两者可串联：先跑注解规则，再跑自定义逻辑。
type MessageValidator func(ctx context.Context, rq any) error

// Validator 在 handler 之前对请求执行校验。
//
// validate 为 nil 时退化为空操作（不做校验）。语义保持显式可见：要么传回调，
// 要么不挂中间件——不存在「挂了中间件却暗中放行」的中间态。gin 面的字段级
// 校验由 gen 域绑定（web.BindProtoRequest 的 buf.validate）承担，本件在统一
// 链上补 gRPC 面（及任何经统一链的自定义路由）的校验位。
func Validator(validate MessageValidator) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			if validate != nil {
				if err := validate(ctx, req); err != nil {
					return nil, err
				}
			}
			return next(ctx, req)
		}
	}
}
