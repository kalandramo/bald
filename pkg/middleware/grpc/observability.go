// Package grpc 提供 bald 框架在 gRPC 拦截器层的可观测性中间件。
//
// 移植自 onexstack/pkg/middleware/grpc，日志输出由 log/slog 改为 bald 的
// pkg/log（项目统一日志契约），其余 trace 注入与路径跳过逻辑保持一致。
package grpc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/pkg/log"
)

// tracer 是 bald gRPC 层使用的 OpenTelemetry tracer。
// 未设置全局 TracerProvider 时，otel.Tracer 返回 no-op tracer，零配置也能运行。
var tracer = otel.Tracer("bald/grpc")

// Standard trace header keys
const (
	// W3C Trace Context standard
	TraceParentHeaderKey = "traceparent"

	// Simple trace ID
	TraceIDHeaderKey = "X-Trace-Id"

	// Generic request ID
	RequestIDHeaderKey = "X-Request-Id"

	// Tracestate
	TraceStateHeaderKey = "tracestate"
)

// TraceInjectionMode defines how trace information is injected
type TraceInjectionMode int

const (
	// InjectW3CTraceContext injects full W3C trace context (recommended)
	InjectW3CTraceContext TraceInjectionMode = iota
	// InjectTraceIDOnly injects only trace ID
	InjectTraceIDOnly
	// InjectBoth injects both W3C format and simple trace ID
	InjectBoth
	// InjectNone disables trace injection
	InjectNone
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

// WithLogger 显式注入自定义 log.Logger（D8：优先于全局惰性解析——注入后运行期
// SetLogger 重建不影响本拦截器，需要该隔离语义时使用）。
func WithLogger(logger log.Logger) Option {
	return func(o *ObservabilityOptions) {
		if logger != nil {
			o.Logger = logger
		}
	}
}

// resolveLogger 返回生效 Logger：显式 WithLogger 注入优先，否则每次调用惰性取
// 全局（D8：构造期快照会让运行期 SetLogger 重建对请求日志永久失效）。
func (o *ObservabilityOptions) resolveLogger() log.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return log.GetLogger()
}

// UnaryObservability is the gRPC unary interceptor with trace injection
func UnaryObservability(opts ...Option) grpc.UnaryServerInterceptor {
	config := &ObservabilityOptions{
		TraceInjectionMode: InjectTraceIDOnly,
		SkipMethods:        []string{"/metrics"},
		DisableBodyLog:     false,
	}

	for _, opt := range opts {
		opt(config)
	}

	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		start := time.Now()

		// Skip logging if configured
		if shouldSkipMethod(info.FullMethod, config.SkipMethods) {
			return handler(ctx, req)
		}

		// 真正起一个 span：上游已有 span 则作为 child，否则新建 root。
		ctx, span := tracer.Start(ctx, info.FullMethod)
		defer span.End()

		// 把 trace_id/span_id 挂到 ctx 属性流，使本请求范围所有日志自动携带。
		ctx = log.ContextWithAttrs(ctx,
			slog.String("trace_id", trace.SpanContextFromContext(ctx).TraceID().String()),
			slog.String("span_id", trace.SpanContextFromContext(ctx).SpanID().String()),
		)

		// Extract trace information early
		spanCtx := trace.SpanContextFromContext(ctx)

		// Inject trace headers into outgoing metadata
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			newMD := injectTraceMetadata(md, spanCtx, config)
			ctx = metadata.NewIncomingContext(ctx, newMD)
		}

		isDebugLevel := config.resolveLogger().Enabled(log.LevelDebug)
		shouldLogBody := isDebugLevel && !config.DisableBodyLog

		var requestInfo any
		var responseInfo any
		if shouldLogBody {
			requestInfo = req
		}

		resp, err := handler(ctx, req)

		if shouldLogBody {
			responseInfo = resp
		}

		duration := time.Since(start).Seconds()

		// 把状态码作为 span 属性，便于链路追踪侧按状态过滤。
		span.SetAttributes(attribute.String("grpc.code", status.Code(err).String()))

		// Build structured log
		event := map[string]any{"duration": duration}
		source := map[string]any{"id": spanCtx.TraceID().String()}
		grpcData := map[string]any{
			"service": info.FullMethod,
			"code":    status.Code(err).String(),
		}
		if shouldLogBody {
			grpcData["request"] = requestInfo
			grpcData["response"] = responseInfo
		}

		// ctx 已携带 trace_id/span_id 属性，日志自动附带。
		if isDebugLevel {
			config.resolveLogger().Debug(ctx, "gRPC request completed",
				"event", event,
				"source", source,
				"grpc", grpcData,
			)
		} else {
			config.resolveLogger().Info(ctx, "gRPC request completed",
				"event", event,
				"source", source,
				"grpc", grpcData,
			)
		}

		return resp, err
	}
}

// StreamObservability is the gRPC stream interceptor with trace injection
func StreamObservability(opts ...Option) grpc.StreamServerInterceptor {
	config := &ObservabilityOptions{
		TraceInjectionMode: InjectTraceIDOnly,
		SkipMethods:        []string{"/metrics"},
		DisableBodyLog:     false,
	}

	for _, opt := range opts {
		opt(config)
	}

	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		start := time.Now()

		// Skip logging if configured
		if shouldSkipMethod(info.FullMethod, config.SkipMethods) {
			return handler(srv, ss)
		}

		// 真正起一个 span，并把 trace_id/span_id 挂到 ctx 属性流。
		ctx := ss.Context()
		ctx, span := tracer.Start(ctx, info.FullMethod)
		defer span.End()
		ctx = log.ContextWithAttrs(ctx,
			slog.String("trace_id", trace.SpanContextFromContext(ctx).TraceID().String()),
			slog.String("span_id", trace.SpanContextFromContext(ctx).SpanID().String()),
		)

		// Extract trace information early
		spanCtx := trace.SpanContextFromContext(ctx)

		// Inject trace headers into outgoing metadata
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			newMD := injectTraceMetadata(md, spanCtx, config)
			ctx = metadata.NewIncomingContext(ctx, newMD)
			ss = &traceStream{ServerStream: ss, ctx: ctx}
		}

		err := handler(srv, ss)

		duration := time.Since(start).Seconds()

		// 把状态码作为 span 属性。
		span.SetAttributes(attribute.String("grpc.code", status.Code(err).String()))

		event := map[string]any{"duration": duration}
		source := map[string]any{"id": spanCtx.TraceID().String()}
		grpcData := map[string]any{
			"service": info.FullMethod,
			"code":    status.Code(err).String(),
		}

		// ctx 已携带 trace_id/span_id 属性，日志自动附带。
		if config.resolveLogger().Enabled(log.LevelDebug) {
			config.resolveLogger().Debug(ctx, "gRPC stream completed",
				"event", event,
				"source", source,
				"grpc", grpcData,
			)
		} else {
			config.resolveLogger().Info(ctx, "gRPC stream completed",
				"event", event,
				"source", source,
				"grpc", grpcData,
			)
		}

		return err
	}
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

	// Wildcard support
	if strings.Contains(pattern, "*") {
		return matchWildcard(method, pattern)
	}

	return false
}

// matchWildcard performs simple wildcard matching
func matchWildcard(text, pattern string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		substr := pattern[1 : len(pattern)-1]
		return strings.Contains(text, substr)
	}
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(text, pattern[1:])
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(text, pattern[:len(pattern)-1])
	}
	return text == pattern
}

// injectTraceMetadata injects trace headers into gRPC metadata
func injectTraceMetadata(md metadata.MD, spanCtx trace.SpanContext, config *ObservabilityOptions) metadata.MD {
	if !spanCtx.IsValid() {
		return md
	}

	traceID := spanCtx.TraceID().String()
	spanID := spanCtx.SpanID().String()

	switch config.TraceInjectionMode {
	case InjectW3CTraceContext:
		traceFlags := "01"
		if !spanCtx.IsSampled() {
			traceFlags = "00"
		}
		traceparent := fmt.Sprintf("00-%s-%s-%s", traceID, spanID, traceFlags)
		md.Set(TraceParentHeaderKey, traceparent)

	case InjectTraceIDOnly:
		headerKey := TraceIDHeaderKey
		if config.CustomTraceHeader != "" {
			headerKey = config.CustomTraceHeader
		}
		md.Set(headerKey, traceID)

	case InjectBoth:
		traceFlags := "01"
		if !spanCtx.IsSampled() {
			traceFlags = "00"
		}
		traceparent := fmt.Sprintf("00-%s-%s-%s", traceID, spanID, traceFlags)
		md.Set(TraceParentHeaderKey, traceparent)

		headerKey := TraceIDHeaderKey
		if config.CustomTraceHeader != "" {
			headerKey = config.CustomTraceHeader
		}
		md.Set(headerKey, traceID)

	case InjectNone:
		// Do nothing
	}

	return md
}

// traceStream wraps grpc.ServerStream to carry the modified context
type traceStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *traceStream) Context() context.Context {
	return s.ctx
}
