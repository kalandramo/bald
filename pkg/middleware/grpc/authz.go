package grpc

import (
	"context"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	berrors "github.com/kalandramo/bald/pkg/berrors"
	"github.com/kalandramo/bald/pkg/log"
)

// AuthzInterceptor 是 gRPC 授权拦截器：用注入的 Authorizer 对 (subject, object, action)
// 做访问控制。subject 取自认证阶段注入的 AuthClaims.Subject；object 取 gRPC 方法名；
// action 固定为 "CALL"（对应 gRPC 调用语义）。
//
// authorizer 为 nil 时拦截器退化为空操作（不授权，等同于开放服务）。
func AuthzInterceptor(authorizer authz.Authorizer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if authorizer == nil {
			return handler(ctx, req)
		}
		subject := ""
		if c := authn.AuthClaimsFromContext(ctx); c != nil {
			subject = c.Subject
		}
		object := ""
		if info != nil {
			object = info.FullMethod
		}
		action := "CALL"

		allowed, err := authorizer.Authorize(ctx, subject, object, action)
		if err != nil {
			e := berrors.Internal("AUTHZ_ENGINE_ERROR").WithMessage("%s", err.Error())
			log.GetLogger().Error(ctx, "authorization engine error", "error", err)
			return nil, e
		}
		if !allowed {
			e := berrors.PermissionDenied("ACCESS_DENIED").WithMessage(
				"access denied: subject=%s, object=%s, action=%s", subject, object, action)
			return nil, e
		}
		return handler(ctx, req)
	}
}
