package gin

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kalandramo/bald/pkg/contextx"
)

// RequestIDMiddleware 是 gin 中间件，确保每个请求都有唯一请求 ID：
// 优先从请求头 X-Request-ID 获取，缺失时生成新的 UUID；并将请求 ID 注入
// context 与响应头，便于链路追踪。
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.Request.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = uuid.New().String()
		}

		ctx := contextx.WithRequestID(c.Request.Context(), requestID)
		c.Request = c.Request.WithContext(ctx)

		c.Writer.Header().Set("X-Request-ID", requestID)
		c.Next()
	}
}
