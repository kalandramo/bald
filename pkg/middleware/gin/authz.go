package gin

import (
	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	berrors "github.com/kalandramo/bald/pkg/berrors"
	"github.com/kalandramo/bald/pkg/berrors/httperr"
	"github.com/kalandramo/bald/pkg/log"
)

// AuthzMiddleware 是 gin 授权中间件：用注入的 Authorizer 对 (subject, object, action)
// 访问控制。subject 取自认证阶段注入的 AuthClaims.Subject；object 取请求路径；
// action 取 HTTP 方法。
//
// authorizer 为 nil 时中间件退化为空操作（不授权，等同于开放服务）。
func AuthzMiddleware(authorizer authz.Authorizer) gin.HandlerFunc {
	return func(c *gin.Context) {
		if authorizer == nil {
			c.Next()
			return
		}
		subject := ""
		if cl := authn.AuthClaimsFromContext(c.Request.Context()); cl != nil {
			subject = cl.Subject
		}
		object := c.Request.URL.Path
		action := c.Request.Method

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
