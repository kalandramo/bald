package gin

import (
	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	berrors "github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/berrors/httperr"
)

// Option 配置 AuthzMiddleware 的请求→授权三元组提取方式（与 pkg/middleware/grpc 对称）。
// 借鉴 go-lulu 的 ResolverFunc 模式，将传输层请求翻译为授权语义的职责外置为可插拔函数。
type AuthzOption func(*authzOptions)

type authzOptions struct {
	// objectResolver 从 HTTP 路径推导 object。nil 时使用默认 object=path。
	objectResolver authz.ObjectResolver
	// actionResolver 从 HTTP 方法推导 action。nil 时默认 action=方法名（GET/DELETE/...）。
	actionResolver authz.ActionResolver
	// subjectResolver 自定义 subject 提取。nil 时默认从 authn.AuthClaims.Subject 取。
	subjectResolver func(c *gin.Context) string
}

// WithObjectResolver 自定义 object 推导；常用 authz.DefaultHTTPObject（去 /v1 前缀）。
func WithObjectResolver(fn authz.ObjectResolver) AuthzOption {
	return func(o *authzOptions) { o.objectResolver = fn }
}

// WithActionResolver 自定义 action 推导；常用 authz.DefaultHTTPAction（小写方法名）。
func WithActionResolver(fn authz.ActionResolver) AuthzOption {
	return func(o *authzOptions) { o.actionResolver = fn }
}

// WithSubjectResolver 自定义 subject 提取。
func WithSubjectResolver(fn func(c *gin.Context) string) AuthzOption {
	return func(o *authzOptions) { o.subjectResolver = fn }
}

// AuthzMiddleware 是 gin 授权中间件：用注入的 Authorizer 对 (subject, object, action)
// 访问控制。
//
// 默认（无 Option）：object 取请求路径、action 取 HTTP 方法（与 P7 一致）。
// 接 casbin/RBAC 且需与 gRPC 共用策略时，可用 authz.DefaultHTTPObject/DefaultHTTPAction
// 做传输中立归一化（与 grpc 侧对称）：
//
//	ginmw.AuthzMiddleware(authorizer,
//	    ginmw.WithObjectResolver(authz.DefaultHTTPObject),
//	    ginmw.WithActionResolver(authz.DefaultHTTPAction),
//	)
//
// authorizer 为 nil 时中间件退化为空操作（不授权，等同于开放服务）。
func AuthzMiddleware(authorizer authz.Authorizer, opts ...AuthzOption) gin.HandlerFunc {
	cfg := &authzOptions{}
	for _, opt := range opts {
		opt(cfg)
	}
	return func(c *gin.Context) {
		if authorizer == nil {
			c.Next()
			return
		}
		subject := ""
		if cfg.subjectResolver != nil {
			subject = cfg.subjectResolver(c)
		} else if cl := authn.AuthClaimsFromContext(c.Request.Context()); cl != nil {
			subject = cl.Subject
		}
		object := c.Request.URL.Path
		if cfg.objectResolver != nil {
			object = cfg.objectResolver(c.Request.URL.Path)
		}
		action := c.Request.Method
		if cfg.actionResolver != nil {
			action = cfg.actionResolver(c.Request.Method)
		}

		log.GetLogger().Info(c.Request.Context(), "authorize",
			"subject", subject, "object", object, "action", action)

		allowed, err := authorizer.Authorize(c.Request.Context(), subject, object, action)
		if err != nil {
			e := berrors.Internal("AUTHZ_ENGINE_ERROR").WithMessage("%s", err.Error())
			c.AbortWithStatusJSON(httperr.StatusCode(e), gin.H{
				"reason":  e.Reason,
				"message": e.Message,
			})
			return
		}
		if !allowed {
			e := berrors.PermissionDenied("ACCESS_DENIED").WithMessage(
				"access denied: subject=%s, object=%s, action=%s", subject, object, action)
			c.AbortWithStatusJSON(httperr.StatusCode(e), gin.H{
				"reason":  e.Reason,
				"message": e.Message,
			})
			return
		}
		c.Next()
	}
}
