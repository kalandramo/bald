package grpc

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/kalandramo/bald/pkg/authn"
	berrors "github.com/kalandramo/bald/pkg/berrors"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/log"
)

// AuthnInterceptor 是 gRPC 认证拦截器：从 metadata 抽取 Bearer token 存入 ctx，
// 交由注入的 Authenticator 校验并把 AuthClaims 写入 ctx（含租户/用户键，供下游
// pkg/store 多租户自动隔离）。
//
// authenticator 为 nil 时拦截器退化为空操作（不认证，等同于公开服务）。
func AuthnInterceptor(authenticator authn.Authenticator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if authenticator == nil {
			return handler(ctx, req)
		}
		token, err := bearerFromMetadata(ctx)
		if err != nil {
			e := berrors.Unauthenticated("MISSING_TOKEN").WithMessage("%s", err.Error())
			log.GetLogger().Error(ctx, "authentication failed", "error", err)
			return nil, e
		}
		ctx = authn.ContextWithToken(ctx, token)

		claims, err := authenticator.Authenticate(ctx)
		if err != nil {
			e := berrors.Unauthenticated("UNAUTHENTICATED").WithMessage("%s", err.Error())
			log.GetLogger().Error(ctx, "authentication failed", "error", err)
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
		return handler(ctx, req)
	}
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
