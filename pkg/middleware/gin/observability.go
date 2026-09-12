// Package gin 提供 bald 框架在 gin HTTP 层的可观测性中间件。
//
// 移植自 onexstack/pkg/middleware/gin，日志输出由 log/slog 改为 bald 的
// pkg/log（项目统一日志契约），其余 trace 注入与路径跳过逻辑保持一致。
package gin

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/middleware"
	"github.com/kalandramo/bald/pkg/middleware/shared"
)

// tracer 是 bald HTTP 层使用的 OpenTelemetry tracer。
// 未设置全局 TracerProvider 时，otel.Tracer 返回 no-op tracer（起 span 不报错、不采样），
// 保证框架在零配置下也能运行——这与"核心零依赖、可观测性由调用方装配"的设计一致。
var tracer = otel.Tracer("bald/gin")

// Standard trace header keys（R1 合并：定义收敛至 middleware/shared，此处
// 别名维持既有导出 API 稳定）
const (
	// W3C Trace Context standard (most recommended)
	TraceParentHeaderKey = shared.TraceParentHeaderKey

	// Simple trace ID (most widely used)
	TraceIDHeaderKey = shared.TraceIDHeaderKey

	// Generic request ID (universal compatibility)
	RequestIDHeaderKey = shared.RequestIDHeaderKey

	// Tracestate for additional context
	TraceStateHeaderKey = shared.TraceStateHeaderKey
)

// TraceInjectionMode defines how trace information is injected（R1 合并：定义收敛至 middleware/shared）
type TraceInjectionMode = shared.TraceInjectionMode

const (
	// InjectW3CTraceContext injects full W3C trace context (recommended)
	InjectW3CTraceContext = shared.InjectW3CTraceContext
	// InjectTraceIDOnly injects only trace ID
	InjectTraceIDOnly = shared.InjectTraceIDOnly
	// InjectBoth injects both W3C format and simple trace ID
	InjectBoth = shared.InjectBoth
	// InjectNone disables trace injection
	InjectNone = shared.InjectNone
)

// ObservabilityOptions holds configuration for trace injection and logging
type ObservabilityOptions struct {
	TraceInjectionMode TraceInjectionMode
	CustomTraceHeader  string     // Custom header name for trace ID
	SkipPaths          []string   // Paths to skip logging (supports wildcards)
	Logger             log.Logger // Custom logger instance
	DisableBodyLog     bool       // Force disable logging of request/response body
}

// Option is a functional option for configuring the middleware
type Option func(*ObservabilityOptions)

// WithLogger 显式注入自定义 log.Logger（D8：优先于全局惰性解析——注入后运行期
// SetLogger 重建不影响本中间件，需要该隔离语义时使用）。
func WithLogger(logger log.Logger) Option {
	return func(o *ObservabilityOptions) {
		if logger != nil {
			o.Logger = logger
		}
	}
}

// WithSkipMetrics is a convenience function to skip common metrics endpoints
func WithSkipMetrics() Option {
	return func(o *ObservabilityOptions) {
		commonPaths := []string{
			"/health",
			"/healthz",
			"/health/*",
			"/ready",
			"/readiness",
			"/live",
			"/liveness",
			"/metrics",
			"/prometheus",
			"/status",
			"/ping",
			"/version",
			"/info",
			"/favicon.ico",
			"/robots.txt",
		}
		o.SkipPaths = append(o.SkipPaths, commonPaths...)
	}
}

// resolveLogger 返回生效 Logger：显式 WithLogger 注入优先，否则每次请求惰性取
// 全局（D8：构造期快照会让运行期 SetLogger 重建对请求日志永久失效——示例先默认
// logger 构造中间件、BeforeStart 再按最终配置重建，快照即分裂）。
func (o *ObservabilityOptions) resolveLogger() log.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return log.GetLogger()
}

// Observability middleware with configurable trace injection
func Observability(opts ...Option) gin.HandlerFunc {
	// Default configuration
	config := &ObservabilityOptions{
		TraceInjectionMode: InjectTraceIDOnly,
		SkipPaths:          []string{"/metrics"}, // Default skip /metrics
		DisableBodyLog:     false,
	}

	// Apply options
	for _, opt := range opts {
		opt(config)
	}

	return func(c *gin.Context) {
		start := time.Now()
		ctx := c.Request.Context()

		// Check if this request should be skipped
		shouldSkip := shouldSkipPath(c.Request.URL.Path, c.Request.Method, config.SkipPaths)
		if shouldSkip {
			c.Next()
			return
		}

		// 真正起一个 span：若上游已有 span（如网关/客户端注入）则作为 child，
		// 否则新建 root。未配置全局 TracerProvider 时为 no-op，零配置也能跑。
		// SpanKindServer：inbound HTTP 的 OTel 语义（spanmetrics 服务端聚合
		// 与服务拓扑图依赖该值，缺省 internal 会被漏计）。
		ctx, span := tracer.Start(ctx, c.Request.Method+" "+c.Request.URL.Path,
			trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		// 把 span 写入请求 ctx，使后续 handler 与日志处于同一 trace 上下文。
		c.Request = c.Request.WithContext(ctx)

		// Extract trace information from the (now live) span context.
		spanCtx := trace.SpanContextFromContext(ctx)

		// 把 trace_id/span_id 挂到 ctx 属性流，使本请求范围内所有经
		// log.GetLogger() 输出的日志自动携带（slog 后端消费 ContextWithAttrs）。
		// no-op tracer（未装配全局 TracerProvider）下 SpanContext 恒全零，
		// LogTraceIDs 兜底随机 ID 作日志关联；真实 tracer 在跑时透传真实值。
		logTraceID, logSpanID := middleware.LogTraceIDs(ctx)
		// 审计-链路关联（R4 复审连带修复）：trace_id 同时进 ctx 属性流（日志
		// 自动携带）与 contextx（audit/authn/crudbridge 的 TraceIDFromContext
		// 消费）——此前只进前者，审计事件 trace_id 字段恒空。与日志同源
		// （LogTraceIDs 产物，no-op tracer 时同为兜底随机 ID）。
		ctx = contextx.WithTraceID(ctx, logTraceID)
		ctx = log.ContextWithAttrs(ctx,
			slog.String("trace_id", logTraceID),
			slog.String("span_id", logSpanID),
		)
		c.Request = c.Request.WithContext(ctx)

		// Inject trace headers based on configuration (unless skipping tracing)
		injectTraceHeaders(c, spanCtx, config)

		var requestBody string
		var responseBuffer bytes.Buffer

		// Only capture body if Debug is enabled for the logger and body logging
		// is NOT explicitly disabled.
		isDebugLevel := config.resolveLogger().Enabled(log.LevelDebug)
		shouldLogBody := isDebugLevel && !config.DisableBodyLog

		if shouldLogBody && c.Request.Body != nil {
			bodyBytes, _ := io.ReadAll(c.Request.Body)
			requestBody = string(bodyBytes)
			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}

		if shouldLogBody {
			writer := &bodyCaptureWriter{ResponseWriter: c.Writer, body: &responseBuffer}
			c.Writer = writer
		}

		c.Next()

		duration := time.Since(start).Seconds()

		// 把状态码作为 span 属性，便于链路追踪侧按状态过滤。
		span.SetAttributes(attribute.Int("http.status_code", c.Writer.Status()))

		// Build structured log
		event := map[string]any{"duration": duration}
		source := map[string]any{"ip": c.ClientIP()}
		httpData := map[string]any{
			"request": map[string]any{
				"method": c.Request.Method,
				"path":   c.Request.URL.Path,
				"id":     logTraceID,
			},
			"response": map[string]any{
				"status_code": c.Writer.Status(),
			},
		}
		userAgent := map[string]any{"original": c.Request.UserAgent()}

		if shouldLogBody {
			httpData["request"].(map[string]any)["body"] = map[string]any{
				"content": requestBody,
				"bytes":   len(requestBody),
			}
			httpData["response"].(map[string]any)["body"] = map[string]any{
				"content": responseBuffer.String(),
				"bytes":   responseBuffer.Len(),
			}
		}

		// Use the configured logger instance. ctx 已携带 trace_id/span_id 属性，
		// 日志自动附带（无需在每条日志手动传 trace_id）。
		if isDebugLevel {
			config.resolveLogger().Debug(ctx, "HTTP request completed",
				"event", event,
				"source", source,
				"http", httpData,
				"user_agent", userAgent,
			)
		} else {
			config.resolveLogger().Info(ctx, "HTTP request completed",
				"event", event,
				"source", source,
				"http", httpData,
				"user_agent", userAgent,
			)
		}
	}
}

// shouldSkipPath checks if a path should be skipped based on configuration
func shouldSkipPath(path, method string, skipPaths []string) bool {
	for _, skipPath := range skipPaths {
		if matchPath(path, method, skipPath) {
			return true
		}
	}
	return false
}

// matchPath matches a request path against a skip pattern
func matchPath(requestPath, method, pattern string) bool {
	// Handle method-specific patterns like "GET /metrics"
	if strings.Contains(pattern, " ") {
		parts := strings.SplitN(pattern, " ", 2)
		if len(parts) == 2 {
			patternMethod := strings.ToUpper(strings.TrimSpace(parts[0]))
			patternPath := strings.TrimSpace(parts[1])

			if patternMethod != strings.ToUpper(method) {
				return false
			}
			return matchPathPattern(requestPath, patternPath)
		}
	}

	// Handle path-only patterns
	return matchPathPattern(requestPath, pattern)
}

// matchPathPattern matches a path against a pattern (supports wildcards)
func matchPathPattern(path, pattern string) bool {
	// Exact match
	if path == pattern {
		return true
	}

	// Wildcard support（R1 合并：通配匹配收敛至 shared.MatchWildcard）
	if strings.Contains(pattern, "*") {
		return shared.MatchWildcard(path, pattern)
	}

	// Prefix match (if pattern ends with /)
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(path, pattern)
	}

	return false
}

// injectTraceHeaders injects trace headers based on configuration（R1 合并：
// header 计算收敛至 shared.InjectTrace，本函数仅剩 gin Header 落点）
func injectTraceHeaders(c *gin.Context, spanCtx trace.SpanContext, config *ObservabilityOptions) {
	shared.InjectTrace(config.TraceInjectionMode, config.CustomTraceHeader, spanCtx, c.Header)
}

// bodyCaptureWriter captures and duplicates written response body
type bodyCaptureWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *bodyCaptureWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}
