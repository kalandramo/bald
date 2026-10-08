package gin

import (
	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/middleware"
)

// Tracing 返回只做链路追踪、不写请求日志的 gin 中间件。
//
// 为什么需要它：契约 `server.http.middleware.tracing` 段此前**零实现**——而
// `Logging()`（= Observability(WithSkipMetrics())）把「起 span / 注入 trace 头」
// 与「写请求日志」耦合在一起，无法只要追踪不要日志。本中间件拆出前者：
//
//   - 起 SpanKindServer span（上游有 span 则作为 child），status 记为 span 属性；
//   - 把 trace_id 写入 ctx 属性流与 contextx（审计/日志关联，与 Observability 同源）；
//   - 按 W3C traceparent 注入响应头（便于前端/网关关联）。
//
// 不写任何请求日志。未配置全局 TracerProvider 时 otel 返回 no-op tracer——
// 起 span 不报错，trace 头仍带兜底 ID（与 Observability 的零配置语义一致）。
//
// 注意与 Logging 的关系：二者都会起 span。若同时挂载，同一请求会有两层 span；
// 契约段一般二选一（要日志用 logging，只要追踪用 tracing），或二者并存接受
// 嵌套 span（tracing 在外、logging 在内，则 logging 的 span 成为 child）。
func Tracing() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		ctx, span := tracer.Start(ctx, c.Request.Method+" "+c.Request.URL.Path)
		defer span.End()

		// trace_id 进 contextx（审计的 TraceIDFromContext 消费）与 ctx 属性流
		// （日志自动携带）。与 observability.go 的落点同源，保证二者可互换。
		logTraceID, logSpanID := middleware.LogTraceIDs(ctx)
		ctx = contextx.WithTraceID(ctx, logTraceID)
		c.Request = c.Request.WithContext(ctx)

		_ = logSpanID // span 属性在 span.End 前由 otel 记录；此处仅需 trace 关联

		c.Next()
	}
}
