package gin

import (
	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/berrors"
	"github.com/kalandramo/bald/pkg/berrors/httperr"
	"github.com/kalandramo/bald/pkg/log"
)

// Authorizer 定义授权接口的抽象。
type Authorizer interface {
	Authorize(subject, object, action string) (bool, error)
}

// AuthzMiddleware 是 gin 授权中间件：基于 subject/object/action 进行访问控制。
func AuthzMiddleware(authorizer Authorizer) gin.HandlerFunc {
	return func(c *gin.Context) {
		subject := contextx.UserIDFromContext(c.Request.Context())
		object := c.Request.URL.Path
		action := c.Request.Method

		log.GetLogger().Info(c.Request.Context(), "build authorize context",
			"subject", subject, "object", object, "action", action)

		allowed, err := authorizer.Authorize(subject, object, action)
		if err != nil || !allowed {
			e := errors.PermissionDenied("ACCESS_DENIED").WithMessage(
				"access denied: subject=%s, object=%s, action=%s, reason=%v",
				subject, object, action, err,
			)
			c.AbortWithStatusJSON(httperr.StatusCode(e), gin.H{
				"reason":  e.Reason,
				"message": e.Message,
			})
			return
		}

		c.Next()
	}
}
