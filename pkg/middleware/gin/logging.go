package gin

import "github.com/gin-gonic/gin"

// Logging 是 gin HTTP 请求日志中间件（基于 Observability，默认注入 trace-id 且
// 跳过健康检查等高频端点）。等价于 onexstack/pkg/middleware/gin 的 Logging。
func Logging() gin.HandlerFunc {
	return Observability(WithSkipMetrics())
}

// RequestID 是 gin 请求 ID 中间件（转发 RequestIDMiddleware）。
func RequestID() gin.HandlerFunc {
	return RequestIDMiddleware()
}
