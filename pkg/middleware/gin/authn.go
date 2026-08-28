package gin

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/errors"
)

// TokenExtractor 从 gin.Context 中解析出用户 ID。具体 token 形态（JWT / session
// 等）由调用方实现，中间件不做业务假设。
type TokenExtractor interface {
	Extract(ctx context.Context, c *gin.Context) (userID string, err error)
}

// UserRetriever 根据用户 ID 获取用户名，用于把身份注入 context。
type UserRetriever interface {
	GetUser(ctx context.Context, userID string) (username string, err error)
}

// AuthnMiddleware 是 gin 认证中间件：解析 token 并将用户信息注入 context。
func AuthnMiddleware(extractor TokenExtractor, retriever UserRetriever) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, err := extractor.Extract(c.Request.Context(), c)
		if err != nil {
			e := errors.Unauthenticated("TOKEN_INVALID").WithMessage("%s", err.Error())
			c.AbortWithStatusJSON(e.StatusCode(), gin.H{
				"reason":  e.Reason,
				"message": e.Message,
			})
			return
		}

		username, err := retriever.GetUser(c.Request.Context(), userID)
		if err != nil {
			e := errors.Unauthenticated("USER_NOT_FOUND").WithMessage("%s", err.Error())
			c.AbortWithStatusJSON(e.StatusCode(), gin.H{
				"reason":  e.Reason,
				"message": e.Message,
			})
			return
		}

		ctx := contextx.WithUserID(c.Request.Context(), userID)
		ctx = contextx.WithUsername(ctx, username)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}
