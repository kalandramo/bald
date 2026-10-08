package gin

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kalandramo/bald/pkg/contextx"
)

// RequestIDConfig 配置请求 ID 中间件，对应契约 request_id 段。
type RequestIDConfig struct {
	// HeaderName 是读取/回写请求 ID 的头名。空值回退默认 "X-Request-ID"。
	// 契约字段 server.http.middleware.request_id.header_name。
	HeaderName string
}

// defaultRequestIDHeader 是被契约注释点名的默认头名（"默认 X-Request-Id"）。
const defaultRequestIDHeader = "X-Request-ID"

// RequestIDMiddleware 是 gin 中间件，确保每个请求都有唯一请求 ID：
// 优先从配置的头（默认 X-Request-ID）获取，缺失时生成新 UUID；并将请求 ID
// 注入 context 与响应头，便于链路追踪。
//
// 可选 RequestIDConfig（变参以保持既有 RequestIDMiddleware() 调用零改动）：
// cfg.HeaderName 覆盖头名（契约 request_id.header_name）。
func RequestIDMiddleware(cfg ...RequestIDConfig) gin.HandlerFunc {
	header := defaultRequestIDHeader
	if len(cfg) > 0 && cfg[0].HeaderName != "" {
		header = cfg[0].HeaderName
	}
	return func(c *gin.Context) {
		requestID := c.Request.Header.Get(header)
		if requestID == "" {
			requestID = uuid.New().String()
		}

		ctx := contextx.WithRequestID(c.Request.Context(), requestID)
		c.Request = c.Request.WithContext(ctx)

		c.Writer.Header().Set(header, requestID)
		c.Next()
	}
}
