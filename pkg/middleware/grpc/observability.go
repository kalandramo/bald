// Package grpc 提供 bald 框架在 gRPC 拦截器层的可观测性中间件。
//
// 移植自 onexstack/pkg/middleware/grpc，日志输出由 log/slog 改为 bald 的
// pkg/log（项目统一日志契约），其余 trace 注入与路径跳过逻辑保持一致。
package grpc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/pkg/log"
)

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
	CustomTraceHeader  string   // Custom header name for trace ID
	SkipMethods        []string // Methods to skip logging (supports wildcards)
	Logger             log.Logger // Custom logger instance
	DisableBodyLog     bool     // Force disable logging of request/response
}

// Option is a functional option for configuring the middleware
type Option func(*ObservabilityOptions)

// WithTraceInjection configures trace injection mode
func WithTraceInjection(mode TraceInjectionMode) Option {
	return func(o *ObservabilityOptions) {
		o.TraceInjectionMode = mode
	}
}

// WithCustomTraceHeader sets a custom header name for trace ID
func WithCustomTraceHeader(headerName string) Option {
	return func(o *ObservabilityOptions) {
		o.CustomTraceHeader = headerName
	}
}

// WithSkipMethods configures methods to skip (supports exact match and wildcards)
func WithSkipMethods(methods ...string) Option {
	return func(o *ObservabilityOptions) {
		o.SkipMethods = append(o.SkipMethods, methods...)
	}
}

// WithLogger configures a custom log.Logger.
// If not provided, log.GetLogger() will be used.
func WithLogger(logger log.Logger) Option {
	return func(o *ObservabilityOptions) {
		if logger != nil {
			o.Logger = logger
		}
	}
}

// WithDisableBodyLog forces the middleware NOT to log request and response,
// even if the log level is Debug.
func WithDisableBodyLog() Option {
	return func(o *ObservabilityOptions) {
		o.DisableBodyLog = true
	}
}

// WithSkipMetrics is a convenience function to skip common health/metrics RPCs
func WithSkipMetrics() Option {
	return func(o *ObservabilityOptions) {
		commonMethods := []string{
			"/health",
			"/healthz",
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
		}
		o.SkipMethods = append(o.SkipMethods, commonMethods...)
	}
}

// UnaryObservability is the gRPC unary interceptor with trace injection
func UnaryObservability(opts ...Option) grpc.UnaryServerInterceptor {
	config := &ObservabilityOptions{
		TraceInjectionMode: InjectTraceIDOnly,
		SkipMethods:        []string{"/metrics"},
		DisableBodyLog:     false,
		Logger:             log.GetLogger(),
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

		// Extract trace information early
		spanCtx := trace.SpanContextFromContext(ctx)

		// Inject trace headers into outgoing metadata
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			newMD := injectTraceMetadata(md, spanCtx, config)
			ctx = metadata.NewIncomingContext(ctx, newMD)
		}

		isDebugLevel := config.Logger.Enabled(log.LevelDebug)
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

		if isDebugLevel {
			config.Logger.Debug(ctx, "gRPC request completed",
				"event", event,
				"source", source,
				"grpc", grpcData,
			)
		} else {
			config.Logger.Info(ctx, "gRPC request completed",
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
		Logger:             log.GetLogger(),
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

		// Extract trace information early
		ctx := ss.Context()
		spanCtx := trace.SpanContextFromContext(ctx)

		// Inject trace headers into outgoing metadata
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			newMD := injectTraceMetadata(md, spanCtx, config)
			ctx = metadata.NewIncomingContext(ctx, newMD)
			ss = &traceStream{ServerStream: ss, ctx: ctx}
		}

		err := handler(srv, ss)

		duration := time.Since(start).Seconds()

		event := map[string]any{"duration": duration}
		source := map[string]any{"id": spanCtx.TraceID().String()}
		grpcData := map[string]any{
			"service": info.FullMethod,
			"code":    status.Code(err).String(),
		}

		if config.Logger.Enabled(log.LevelDebug) {
			config.Logger.Debug(ctx, "gRPC stream completed",
				"event", event,
				"source", source,
				"grpc", grpcData,
			)
		} else {
			config.Logger.Info(ctx, "gRPC stream completed",
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

// Convenience functions for common configurations

// UnaryObservabilityWithW3CTraceContext creates unary interceptor with W3C context
func UnaryObservabilityWithW3CTraceContext() grpc.UnaryServerInterceptor {
	config := &ObservabilityOptions{TraceInjectionMode: InjectW3CTraceContext}
	return UnaryObservability(WithTraceInjection(config.TraceInjectionMode))
}

// UnaryObservabilityWithTraceID creates unary interceptor with simple trace ID
func UnaryObservabilityWithTraceID() grpc.UnaryServerInterceptor {
	return UnaryObservability(WithTraceInjection(InjectTraceIDOnly))
}

// UnaryObservabilityWithCustomHeader creates unary interceptor with custom header
func UnaryObservabilityWithCustomHeader(headerName string) grpc.UnaryServerInterceptor {
	return UnaryObservability(
		WithTraceInjection(InjectTraceIDOnly),
		WithCustomTraceHeader(headerName),
	)
}

// UnaryObservabilitySkipMetrics creates unary interceptor that skips metrics RPCs
func UnaryObservabilitySkipMetrics() grpc.UnaryServerInterceptor {
	return UnaryObservability(WithSkipMetrics())
}

// UnaryObservabilityWithSkipMethods creates unary interceptor with custom skip methods
func UnaryObservabilityWithSkipMethods(methods ...string) grpc.UnaryServerInterceptor {
	return UnaryObservability(WithSkipMethods(methods...))
}

// StreamObservabilityWithW3CTraceContext creates stream interceptor with W3C context
func StreamObservabilityWithW3CTraceContext() grpc.StreamServerInterceptor {
	return StreamObservability(WithTraceInjection(InjectW3CTraceContext))
}

// StreamObservabilityWithTraceID creates stream interceptor with simple trace ID
func StreamObservabilityWithTraceID() grpc.StreamServerInterceptor {
	return StreamObservability(WithTraceInjection(InjectTraceIDOnly))
}

// StreamObservabilityWithCustomHeader creates stream interceptor with custom header
func StreamObservabilityWithCustomHeader(headerName string) grpc.StreamServerInterceptor {
	return StreamObservability(
		WithTraceInjection(InjectTraceIDOnly),
		WithCustomTraceHeader(headerName),
	)
}

// StreamObservabilitySkipMetrics creates stream interceptor that skips metrics RPCs
func StreamObservabilitySkipMetrics() grpc.StreamServerInterceptor {
	return StreamObservability(WithSkipMetrics())
}

// StreamObservabilityWithSkipMethods creates stream interceptor with custom skip methods
func StreamObservabilityWithSkipMethods(methods ...string) grpc.StreamServerInterceptor {
	return StreamObservability(WithSkipMethods(methods...))
}
