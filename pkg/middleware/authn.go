// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// authn.go 是统一形态的认证中间件（自两链合并；方案 S 批次 2）。

package middleware

import (
	"context"
	"strings"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/crudbridge"
)

// AuthnOption 配置统一认证中间件。
type AuthnOption func(*authnOptions)

type authnOptions struct {
	// auditor 指定认证失败审计事件的接收后端；nil 时退化为 NopAuditor。
	// 由 bundle 注入（与请求审计同一后端），保证 authn 路径与业务审计同源。
	auditor audit.Auditor
}

// AuthnWithAuditor 指定认证失败审计后端；缺省 NopAuditor（显式注入是唯一通道）。
func AuthnWithAuditor(a audit.Auditor) AuthnOption {
	return func(o *authnOptions) { o.auditor = a }
}

// Authn 是统一认证中间件：从 Transporter 的请求头抽取 Bearer token 存入 ctx，
// 交由注入的 Authenticator 校验并把 AuthClaims 写入 ctx（含租户/用户键，供
// 下游 pkg/store 多租户自动隔离）。
//
// 一次实现双协议：token 经 Transporter.RequestHeader().Get("authorization")
// 读取（HTTP 头大小写无关、metadata 经大小写归一——调用方零感知）；失败经
// 统一链返回 berrors.Unauthenticated，gRPC 面由桥的 grpcerr 收口保全语义、
// gin 面由桥渲染 401。
//
// 认证失败（缺 token / 校验失败）时发送一条 ResultDeny 审计事件（审计注册在
// Authn 内侧，失败后不再执行，必须由本层显式留痕）。
//
// authenticator 为 nil 时中间件退化为空操作（不认证，等同于公开服务）。
func Authn(authenticator authn.Authenticator, opts ...AuthnOption) Middleware {
	cfg := &authnOptions{}
	for _, opt := range opts {
		opt(cfg)
	}
	auditor := cfg.auditor
	if auditor == nil {
		auditor = audit.NopAuditor()
	}
	return func(next Handler) Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			if authenticator == nil {
				return next(ctx, req)
			}
			token, err := bearerFromTransporter(ctx)
			if err != nil {
				e := berrors.Unauthenticated("MISSING_TOKEN").WithMessage("%s", err.Error())
				log.Error(ctx, "authentication failed", "error", err)
				auditAuthnFailure(ctx, auditor, e.Reason)
				return nil, e
			}
			ctx = authn.ContextWithToken(ctx, token)

			claims, err := authenticator.Authenticate(ctx)
			if err != nil {
				e := berrors.Unauthenticated("UNAUTHENTICATED").WithMessage("%s", err.Error())
				log.Error(ctx, "authentication failed", "error", err)
				auditAuthnFailure(ctx, auditor, e.Reason)
				return nil, e
			}
			// 过期校验已下沉至 Authenticator.Authenticate（实现契约必须校验
			// ExpiresAt），中间件不再重复判断。

			ctx = authn.ContextWithAuthClaims(ctx, claims)
			// 租户 ID 写入 contextx 供 pkg/store 多租户自动隔离——认证层是唯一
			// 可信的租户来源。Viewer 注入供 crudbridge EnforceTenant/DataScope。
			if claims.TenantID != "" {
				ctx = contextx.WithTenantID(ctx, claims.TenantID)
			}
			ctx = crudbridge.InjectViewerFromIdentity(ctx,
				claims.Subject, claims.TenantID, contextx.TraceIDFromContext(ctx),
				claims.Scopes, claims.Roles, claims.Platform)
			return next(ctx, req)
		}
	}
}

// auditAuthnFailure 在 authn 失败路径显式发一条 ResultDeny 审计事件（旁路
// 语义，绝不影响 Unauthenticated 返回本身）。operation 取自 Transporter
// （gRPC 完整方法名或 "METHOD /路由模板"，双协议同源）。Subject 为空
// （认证失败无 claims）。meta 的 path 键仅 HTTP 面携带（Transporter.Path
// 非空时；gRPC 无 path 概念，meta 形状保持不含该键）。
func auditAuthnFailure(ctx context.Context, auditor audit.Auditor, reason string) {
	operation := "unknown"
	meta := map[string]any{
		"reason":     reason,
		"operation":  operation,
		"request_id": contextx.RequestIDFromContext(ctx),
	}
	if tr, ok := FromServerContext(ctx); ok {
		operation = tr.Operation()
		meta["operation"] = operation
		if p := tr.Path(); p != "" {
			meta["path"] = p
		}
	}
	recordSafely(auditor, ctx, audit.AuditEvent{
		Object: "authn",
		Action: "authenticate",
		Result: audit.ResultDeny,
		Error:  reason,
		Meta:   meta,
	})
}

// bearerFromTransporter 从 Transporter 请求头的 Authorization 取 Bearer token。
func bearerFromTransporter(ctx context.Context) (string, error) {
	tr, ok := FromServerContext(ctx)
	if !ok {
		return "", errNoToken
	}
	vals := tr.RequestHeader().Values("authorization")
	if len(vals) == 0 || vals[0] == "" {
		return "", errNoToken
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(vals[0], prefix) {
		return "", errNoToken
	}
	return strings.TrimPrefix(vals[0], prefix), nil
}

type tokenErr struct{ s string }

func (e tokenErr) Error() string { return e.s }

var errNoToken = tokenErr{"missing or malformed Authorization bearer token"}
