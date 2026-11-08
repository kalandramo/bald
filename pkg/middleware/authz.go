// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// authz.go 是统一形态的授权中间件（自两链合并；方案 S 批次 2）。

package middleware

import (
	"context"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
)

// AuthzOption 配置统一授权中间件的请求→授权三元组提取方式。把传输层请求
// 翻译为授权语义的职责外置为可插拔 Resolver——REST（"GET /v1/secret/*"）与
// gRPC（"SecretService.*"）经 authz.DefaultHTTPObject/DefaultGRPCObject 归一
// 到同一资源名+动作，策略无需双写。
type AuthzOption func(*authzOptions)

type authzOptions struct {
	// objectResolver 从 operation（Transporter.Operation()：gRPC 完整方法名或
	// "METHOD /路由模板"）推导 object。nil 时默认 object=operation 本身。
	objectResolver authz.ObjectResolver
	// actionResolver 从 operation 推导 action。nil 时默认：HTTP 取动词（GET/
	// POST…），gRPC 取 "CALL"。
	actionResolver authz.ActionResolver
	// subjectResolver 自定义 subject 提取。nil 时默认从 AuthClaims.Subject 取。
	subjectResolver func(ctx context.Context, operation string) string
}

// WithObjectResolver 自定义 object 推导。
func WithObjectResolver(fn authz.ObjectResolver) AuthzOption {
	return func(o *authzOptions) { o.objectResolver = fn }
}

// WithActionResolver 自定义 action 推导。
func WithActionResolver(fn authz.ActionResolver) AuthzOption {
	return func(o *authzOptions) { o.actionResolver = fn }
}

// WithSubjectResolver 自定义 subject 提取。
func WithSubjectResolver(fn func(ctx context.Context, operation string) string) AuthzOption {
	return func(o *authzOptions) { o.subjectResolver = fn }
}

// Authz 是统一授权中间件：用注入的 Authorizer 对 (subject, object, action)
// 做访问控制。
//
// 默认（无 Option）：
//   - subject 取自认证阶段注入的 AuthClaims.Subject；
//   - object = Transporter.Operation()（gRPC 完整方法名 / "METHOD /路由模板"）；
//   - action：HTTP 取动词（GET/POST…），gRPC 取 "CALL"（向后兼容）。
//
// authorizer 为 nil 时中间件退化为空操作（不授权，等同于开放服务）。
func Authz(authorizer authz.Authorizer, opts ...AuthzOption) Middleware {
	cfg := &authzOptions{}
	for _, opt := range opts {
		opt(cfg)
	}
	return func(next Handler) Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			if authorizer == nil {
				return next(ctx, req)
			}
			operation := ""
			if tr, ok := FromServerContext(ctx); ok {
				operation = tr.Operation()
			}

			subject := ""
			if cfg.subjectResolver != nil {
				subject = cfg.subjectResolver(ctx, operation)
			} else if c := authn.AuthClaimsFromContext(ctx); c != nil {
				subject = c.Subject
			}

			object := operation
			if cfg.objectResolver != nil {
				object = cfg.objectResolver(operation)
			}

			action := "CALL"
			if tr, ok := FromServerContext(ctx); ok && tr.Method() != "" {
				action = tr.Method()
			}
			if cfg.actionResolver != nil {
				action = cfg.actionResolver(operation)
			}

			allowed, err := authorizer.Authorize(ctx, subject, object, action)
			if err != nil {
				e := berrors.Internal("AUTHZ_ENGINE_ERROR").WithMessage("%s", err.Error())
				log.Error(ctx, "authorization engine error", "error", err)
				return nil, e
			}
			if !allowed {
				e := berrors.PermissionDenied("ACCESS_DENIED").WithMessage(
					"access denied: subject=%s, object=%s, action=%s", subject, object, action)
				return nil, e
			}
			return next(ctx, req)
		}
	}
}
