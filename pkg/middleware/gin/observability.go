// Package gin 提供 bald 框架在 gin HTTP 层的中间件（方案 S 前的协议耦合实现）。
//
// 过渡状态（方案 S 批次 4）：除 HTTP 专属常驻件（CORS/Secure——统一链刻意
// 不收 HTTP 面，见 transport 卡 r05）外，本包中间件随 Bundle.Gin() 旧链在
// 下一 major 移除，统一实现见 pkg/middleware（Recovery/RequestID/Timeout/
// Authn/Authz/Audit/RateLimit/Observability 等九件 + 三桥 + matcher）。
//
// 可观测性已先行并入统一链 middleware.Observability（方案 S 批次 2）：本文件
// 保公共 Option 面与跳过 matcher，经 middleware.GinHandler 桥委托。日志输出
// 走 bald 的 pkg/log，trace 注入原语在 middleware 统一链。
//
// 符号级 Deprecated 标注随物理删除同期进行：旧链门面尚在跨包调用本包，
// 提前标注会触发 staticcheck SA1019 打红 bundle。
package gin

import (
	"strings"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/middleware"
)

// tracer 是 bald HTTP 层使用的 OpenTelemetry tracer。
// 未设置全局 TracerProvider 时，otel.Tracer 返回 no-op tracer（起 span 不报错、不采样），
// 保证框架在零配置下也能运行。委托时传入统一件，保住同包测试替换包级 tracer 的能力。
var tracer = otel.Tracer("bald/gin")

// Standard trace header keys（定义在 middleware 统一链，此处
// 为别名维持既有导出 API 稳定）
const (
	// W3C Trace Context standard (most recommended)
	TraceParentHeaderKey = middleware.TraceParentHeaderKey

	// Simple trace ID (most widely used)
	TraceIDHeaderKey = middleware.TraceIDHeaderKey

	// Generic request ID (universal compatibility)
	RequestIDHeaderKey = middleware.RequestIDHeaderKey

	// Tracestate for additional context
	TraceStateHeaderKey = middleware.TraceStateHeaderKey
)

// TraceInjectionMode defines how trace information is injected（定义在 middleware 统一链）
type TraceInjectionMode = middleware.TraceInjectionMode

const (
	// InjectW3CTraceContext injects full W3C trace context (recommended)
	InjectW3CTraceContext = middleware.InjectW3CTraceContext
	// InjectTraceIDOnly injects only trace ID
	InjectTraceIDOnly = middleware.InjectTraceIDOnly
	// InjectBoth injects both W3C format and simple trace ID
	InjectBoth = middleware.InjectBoth
	// InjectNone disables trace injection
	InjectNone = middleware.InjectNone
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

// WithLogger 显式注入自定义 log.Logger（优先于全局惰性解析——注入后运行期
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

// WithSkipPaths 追加不记录日志的路径（支持通配，如 "/health/*"）。
// 契约 server.http.middleware.logging.skip_paths 的落点。语义为**追加**——
// 与 WithSkipMetrics 的内置表并列（契约段是「额外再跳过哪些」）。
func WithSkipPaths(paths ...string) Option {
	return func(o *ObservabilityOptions) {
		o.SkipPaths = append(o.SkipPaths, paths...)
	}
}

// Observability middleware with configurable trace injection。
// 委托统一链 middleware.Observability（经 GinHandler 桥）：span/trace_id/头注入/
// 日志核心一份实现，本函数仅翻译 gin Option 面与路径+动词跳过 matcher。
func Observability(opts ...Option) gin.HandlerFunc {
	config := &ObservabilityOptions{
		TraceInjectionMode: InjectTraceIDOnly,
		SkipPaths:          []string{"/metrics"}, // Default skip /metrics
		DisableBodyLog:     false,
	}
	for _, opt := range opts {
		opt(config)
	}

	skipPaths := config.SkipPaths
	return middleware.GinHandler(middleware.Observability(
		middleware.ObservabilityWithTracer(tracer),
		middleware.ObservabilityWithTraceInjection(config.TraceInjectionMode, config.CustomTraceHeader),
		middleware.ObservabilityWithLogger(config.Logger),
		middleware.ObservabilityWithDisableBodyLog(config.DisableBodyLog),
		middleware.ObservabilityWithSkip(func(tr middleware.Transporter) bool {
			return shouldSkipPath(tr.Path(), tr.Method(), skipPaths)
		}),
	))
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

	// Wildcard support（通配匹配在 middleware.MatchWildcard）
	if strings.Contains(pattern, "*") {
		return middleware.MatchWildcard(path, pattern)
	}

	// Prefix match (if pattern ends with /)
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(path, pattern)
	}

	return false
}