package grpc

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	berrors "github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/crudbridge"
)

// AuthnOption 配置 AuthnInterceptor。
type AuthnOption func(*authnOptions)

type authnOptions struct {
	// auditor 指定认证失败审计事件的接收后端；nil 时用 audit.GetAuditor() 全局实例。
	// 由 bundle 注入（与请求审计同一后端），保证 authn 路径与业务审计同源。
	auditor audit.Auditor
}

// AuthnWithAuditor 指定认证失败审计后端；缺省用全局 audit.GetAuditor()。
func AuthnWithAuditor(a audit.Auditor) AuthnOption {
	return func(o *authnOptions) { o.auditor = a }
}

// AuthnInterceptor 是 gRPC 认证拦截器：从 metadata 抽取 Bearer token 存入 ctx，
// 交由注入的 Authenticator 校验并把 AuthClaims 写入 ctx（含租户/用户键，供下游
// pkg/store 多租户自动隔离）。
//
// 认证失败（缺 token / 校验失败）时发送一条 ResultDeny 审计事件后返回 Unauthenticated
// ——审计拦截器注册在 Authn 内侧，认证失败时不再执行，必须由本层显式留痕（D3）。
//
// authenticator 为 nil 时拦截器退化为空操作（不认证，等同于公开服务）。
func AuthnInterceptor(authenticator authn.Authenticator, opts ...AuthnOption) grpc.UnaryServerInterceptor {
	cfg := &authnOptions{}
	for _, opt := range opts {
		opt(cfg)
	}
	auditor := cfg.auditor
	if auditor == nil {
		auditor = audit.GetAuditor()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if authenticator == nil {
			return handler(ctx, req)
		}
		token, err := bearerFromMetadata(ctx)
		if err != nil {
			e := berrors.Unauthenticated("MISSING_TOKEN").WithMessage("%s", err.Error())
			log.GetLogger().Error(ctx, "authentication failed", "error", err)
			auditAuthnFailure(ctx, info, auditor, e.Reason)
			return nil, e
		}
		ctx = authn.ContextWithToken(ctx, token)

		claims, err := authenticator.Authenticate(ctx)
		if err != nil {
			e := berrors.Unauthenticated("UNAUTHENTICATED").WithMessage("%s", err.Error())
			log.GetLogger().Error(ctx, "authentication failed", "error", err)
			auditAuthnFailure(ctx, info, auditor, e.Reason)
			return nil, e
		}
		// 过期校验已下沉至 Authenticator.Authenticate（实现契约必须校验 ExpiresAt），
		// 拦截器不再重复判断。

		ctx = authn.ContextWithAuthClaims(ctx, claims)
		// 把租户 ID 写入 contextx，供 pkg/store 多租户隔离（Where.T(ctx)）自动读取。
		// 认证层是唯一可信的租户来源，业务 handler 无需手写租户过滤条件。
		if claims.TenantID != "" {
			ctx = contextx.WithTenantID(ctx, claims.TenantID)
		}
		// 内置注入 viewer.Context（bald-crud EnforceTenant / DataScope 的身份来源）：
		// scopes 映射为权限（"user:read" 格式对上 HasPermission），业务零配置。
		ctx = crudbridge.InjectViewerFromIdentity(ctx,
			claims.Subject, claims.TenantID, contextx.TraceIDFromContext(ctx),
			claims.Scopes, claims.Roles)
		return handler(ctx, req)
	}
}

// auditAuthnFailure 在 authn 失败路径显式发一条 ResultDeny 审计事件（旁路语义，
// panic/错误仅记日志，绝不影响 Unauthenticated 返回本身）。Subject 为空（认证失败无 claims）。
func auditAuthnFailure(ctx context.Context, info *grpc.UnaryServerInfo, auditor audit.Auditor, reason string) {
	fullMethod := ""
	if info != nil {
		fullMethod = info.FullMethod
	}
	recordSafely(auditor, ctx, audit.AuditEvent{
		Object: "authn",
		Action: "authenticate",
		Result: audit.ResultDeny,
		Error:  reason,
		Meta: map[string]any{
			"reason":      reason,
			"full_method": fullMethod,
			"request_id":  contextx.RequestIDFromContext(ctx),
		},
	})
}

// bearerFromMetadata 从 gRPC incoming metadata 的 Authorization 头取 Bearer token。
func bearerFromMetadata(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", errNoToken
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return "", errNoToken
	}
	const prefix = "Bearer "
	t := vals[0]
	if !strings.HasPrefix(t, prefix) {
		return "", errNoToken
	}
	return strings.TrimPrefix(t, prefix), nil
}

type tokenErr struct{ s string }

func (e tokenErr) Error() string { return e.s }

var errNoToken = tokenErr{"missing or malformed Authorization bearer token"}
