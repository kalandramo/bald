package gin

import (
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"

	"github.com/kalandramo/bald/pkg/contextx"
)

// TraceContext 是 gin 中间件，将当前 OpenTelemetry span 的 TraceID 注入
// request context，使后续 handler 可通过 contextx.TraceIDFromContext 获取。
func TraceContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		traceID := trace.SpanFromContext(c.Request.Context()).SpanContext().TraceID().String()
		ctx := contextx.WithTraceID(c.Request.Context(), traceID)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
