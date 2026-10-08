package gin

import "github.com/gin-gonic/gin"

// LoggingConfig 配置请求日志中间件，对应契约 logging 段。
type LoggingConfig struct {
	// SkipPaths 是不记录日志的路径（支持通配，如 "/health/*"）。
	// 契约字段 server.http.middleware.logging.skip_paths。
	//
	// 语义：**追加**在内置的跳过大表（WithSkipMetrics 的健康/指标端点）之后
	// ——契约段是「额外再跳过哪些」，而非「只跳过哪些」。空则等价于既有
	// Logging()（仅内置跳过表）。
	SkipPaths []string
}

// Logging 是 gin HTTP 请求日志中间件（基于 Observability，默认注入 trace-id 且
// 跳过健康检查等高频端点）。等价于 onexstack/pkg/middleware/gin 的 Logging。
//
// 可选 LoggingConfig（变参以保持既有 Logging() 调用零改动）：
// cfg.SkipPaths 追加契约声明的跳过路径（logging.skip_paths）。
func Logging(cfg ...LoggingConfig) gin.HandlerFunc {
	opts := []Option{WithSkipMetrics()}
	if len(cfg) > 0 && len(cfg[0].SkipPaths) > 0 {
		opts = append(opts, WithSkipPaths(cfg[0].SkipPaths...))
	}
	return Observability(opts...)
}
