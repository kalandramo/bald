package grpc

import (
	"context"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	berrors "github.com/kalandramo/bald/pkg/berrors"
	"github.com/kalandramo/bald/pkg/log"
)

// Option 配置 AuthzInterceptor 的请求→授权三元组（subject/object/action）提取方式。
// 借鉴 go-lulu 的 ResolverFunc 模式，将传输层请求翻译为授权语义的职责从拦截器
// 内部（原本焊死 action="CALL"）外置为可插拔函数，使 REST 与 gRPC 可共用同一份
// RBAC 策略（见 pkg/authz 的 DefaultGRPCObject/DefaultGRPCAction 归一化原语）。
type AuthzOption func(*authzOptions)

type authzOptions struct {
	// objectResolver 从 FullMethod 推导 object。nil 时使用默认 object=FullMethod
	// （向后兼容老策略依赖 "CALL" + 原始 FullMethod 的场景）。
	objectResolver authz.ObjectResolver
	// actionResolver 从 FullMethod 推导 action。nil 时默认 action="CALL"。
	actionResolver authz.ActionResolver
	// subjectResolver 自定义 subject 提取（如从 header/claims 其它字段）。
	// nil 时默认从 authn.AuthClaims.Subject 取。
	subjectResolver func(ctx context.Context, info *grpc.UnaryServerInfo) string
}

// WithObjectResolver 自定义 object 推导。
func WithObjectResolver(fn authz.ObjectResolver) AuthzOption {
	return func(o *authzOptions) { o.objectResolver = fn }
}

// WithActionResolver 自定义 action 推导；常用 authz.DefaultGRPCAction（与方法名同源）。
func WithActionResolver(fn authz.ActionResolver) AuthzOption {
	return func(o *authzOptions) { o.actionResolver = fn }
}

// WithSubjectResolver 自定义 subject 提取。
func WithSubjectResolver(fn func(ctx context.Context, info *grpc.UnaryServerInfo) string) AuthzOption {
	return func(o *authzOptions) { o.subjectResolver = fn }
}

// AuthzInterceptor 是 gRPC 授权拦截器：用注入的 Authorizer 对 (subject, object, action)
// 做访问控制。
//
// 默认（无 Option）：
//   - subject 取自认证阶段注入的 AuthClaims.Subject；
//   - object = info.FullMethod（原始全方法名）；
//   - action = "CALL"（对应 gRPC 调用语义，与架构演进路线 P7 一致、向后兼容）。
//
// 传输中立归一化（推荐接 casbin/RBAC 时使用）：
//
//	grpcmw.AuthzInterceptor(authorizer,
//	    grpcmw.WithObjectResolver(authz.DefaultGRPCObject),
//	    grpcmw.WithActionResolver(authz.DefaultGRPCAction),
//	)
//
// 此时同一业务资源在 REST（/v1/secret/*）与 gRPC（SecretService.*）下归一到同一
// 资源名+动作，策略无需双写（M6.8 CR Issue4 的反哺修复）。
//
// authorizer 为 nil 时拦截器退化为空操作（不授权，等同于开放服务）。
func AuthzInterceptor(authorizer authz.Authorizer, opts ...AuthzOption) grpc.UnaryServerInterceptor {
	cfg := &authzOptions{}
	for _, opt := range opts {
		opt(cfg)
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if authorizer == nil {
			return handler(ctx, req)
		}
		subject := ""
		if cfg.subjectResolver != nil {
			subject = cfg.subjectResolver(ctx, info)
		} else if c := authn.AuthClaimsFromContext(ctx); c != nil {
			subject = c.Subject
		}
		object := ""
		if info != nil {
			object = info.FullMethod
			if cfg.objectResolver != nil {
				object = cfg.objectResolver(info.FullMethod)
			}
		}
		action := "CALL"
		if cfg.actionResolver != nil && info != nil {
			action = cfg.actionResolver(info.FullMethod)
		}

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
