// Package grpc 提供 bald 框架在 gRPC 拦截器层的可观测性中间件。
//
// 实现已并入统一链 middleware.Observability（方案 S 批次 2）：本文件保公共
// Option 面与方法跳过 matcher，经 middleware.{Unary,Stream}ServerInterceptor
// 桥委托。日志输出走 bald 的 pkg/log，trace 注入原语在 middleware 统一链。
package grpc

import (
	"strings"

	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/middleware"
)

// tracer 是 bald gRPC 层使用的 OpenTelemetry tracer。
// 未设置全局 TracerProvider 时，otel.Tracer 返回 no-op tracer，零配置也能运行。
// 委托时传入统一件，保住同包测试替换包级 tracer 的能力。
var tracer = otel.Tracer("bald/grpc")

// Standard trace header keys（定义在 middleware 统一链，此处
// 为别名维持既有导出 API 稳定）
const (
	// W3C Trace Context standard
	TraceParentHeaderKey = middleware.TraceParentHeaderKey

	// Simple trace ID
	TraceIDHeaderKey = middleware.TraceIDHeaderKey

	// Generic request ID
	RequestIDHeaderKey = middleware.RequestIDHeaderKey

	// Tracestate
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
	SkipMethods        []string   // Methods to skip logging (supports wildcards)
	Logger             log.Logger // Custom logger instance
	DisableBodyLog     bool       // Force disable logging of request/response
}

// Option is a functional option for configuring the middleware
type Option func(*ObservabilityOptions)

// WithLogger 显式注入自定义 log.Logger（优先于全局惰性解析——注入后运行期
// SetLogger 重建不影响本拦截器，需要该隔离语义时使用）。
func WithLogger(logger log.Logger) Option {
	return func(o *ObservabilityOptions) {
		if logger != nil {
			o.Logger = logger
		}
	}
}

// observabilityMiddleware 把 grpc Option 面翻译成统一件配置（unary/stream 共用）。
func observabilityMiddleware(config *ObservabilityOptions, extra ...middleware.ObservabilityOption) middleware.Middleware {
	skipMethods := config.SkipMethods
	opts := []middleware.ObservabilityOption{
		middleware.ObservabilityWithTracer(tracer),
		middleware.ObservabilityWithTraceInjection(config.TraceInjectionMode, config.CustomTraceHeader),
		middleware.ObservabilityWithLogger(config.Logger),
		middleware.ObservabilityWithDisableBodyLog(config.DisableBodyLog),
		middleware.ObservabilityWithSkip(func(tr middleware.Transporter) bool {
			return shouldSkipMethod(tr.Operation(), skipMethods)
		}),
	}
	opts = append(opts, extra...)
	return middleware.Observability(opts...)
}

func defaultOptions() *ObservabilityOptions {
	return &ObservabilityOptions{
		TraceInjectionMode: InjectTraceIDOnly,
		SkipMethods:        []string{"/metrics"},
		DisableBodyLog:     false,
	}
}

// UnaryObservability is the gRPC unary interceptor with trace injection。
// 委托统一链（经 UnaryServerInterceptor 桥）；unary 记 req/resp 对象作 body。
func UnaryObservability(opts ...Option) grpc.UnaryServerInterceptor {
	config := defaultOptions()
	for _, opt := range opts {
		opt(config)
	}
	return middleware.UnaryServerInterceptor(observabilityMiddleware(config))
}

// StreamObservability is the gRPC stream interceptor with trace injection。
// 委托统一链（经 StreamServerInterceptor 桥）；流式不记 body（req 是 srv 而非
// 消息），完成日志用 "gRPC stream completed"。桥用链传播的 ctx 重包流，span/
// trace_id 注入对整类流生效（含无 incoming metadata 的流）。
func StreamObservability(opts ...Option) grpc.StreamServerInterceptor {
	config := defaultOptions()
	for _, opt := range opts {
		opt(config)
	}
	return middleware.StreamServerInterceptor(observabilityMiddleware(config, middleware.ObservabilityWithStream()))
}

// shouldSkipMethod checks if a method should be skipped
func shouldSkipMethod(method string, skipMethods []string) bool {
	for _, skipMethod := range skipMethods {
		if matchMethod(method, skipMethod) {
			return true
		}
	}
	return false
}

// matchMethod matches a method against a skip pattern (supports wildcards)
func matchMethod(method, pattern string) bool {
	// Exact match
	if method == pattern {
		return true
	}

	// Wildcard support（通配匹配在 middleware.MatchWildcard）
	if strings.Contains(pattern, "*") {
		return middleware.MatchWildcard(method, pattern)
	}

	return false
}
