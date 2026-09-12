// Package shared 收敛 gin/gRPC 可观测性中间件的传输无关核心（trace header
// 常量、注入模式、header 计算、通配匹配）——R1 合并前两份 observability.go
// 逐行重复，SpanKind 等修复须改两份（重复税实缴）；此后改一处两传输层生效。
//
// 两侧 gin/grpc 子包以类型别名 + 常量别名维持既有导出 API 稳定。
package shared

import (
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// Standard trace header keys
const (
	// TraceParentHeaderKey W3C Trace Context standard (most recommended)
	TraceParentHeaderKey = "traceparent"

	// TraceIDHeaderKey simple trace ID (most widely used)
	TraceIDHeaderKey = "X-Trace-Id"

	// RequestIDHeaderKey generic request ID (universal compatibility)
	RequestIDHeaderKey = "X-Request-Id"

	// TraceStateHeaderKey tracestate for additional context
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

// InjectTrace 按 mode 与 spanCtx 计算待注入的 trace header 并经 set 写出
// （gin 侧直接传 c.Header——签名恰好 func(key, value string)；gRPC 侧传
// md.Set 的单值闭包）。spanCtx 无效（no-op tracer 未装配全局
// TracerProvider）时不注入，与合并前两侧行为一致。
func InjectTrace(mode TraceInjectionMode, customTraceHeader string, spanCtx trace.SpanContext, set func(key, value string)) {
	if !spanCtx.IsValid() {
		return
	}

	traceID := spanCtx.TraceID().String()
	spanID := spanCtx.SpanID().String()

	setTraceparent := func() {
		traceFlags := "01" // sampled
		if !spanCtx.IsSampled() {
			traceFlags = "00" // not sampled
		}
		set(TraceParentHeaderKey, fmt.Sprintf("00-%s-%s-%s", traceID, spanID, traceFlags))
	}
	setTraceID := func() {
		headerKey := TraceIDHeaderKey
		if customTraceHeader != "" {
			headerKey = customTraceHeader
		}
		set(headerKey, traceID)
	}

	switch mode {
	case InjectW3CTraceContext:
		setTraceparent()
	case InjectTraceIDOnly:
		setTraceID()
	case InjectBoth:
		setTraceparent()
		setTraceID()
	case InjectNone:
		// Do nothing
	}
}

// MatchWildcard 通配匹配：`*` 全匹配；`*suffix` / `prefix*` / `*substr*`
// 前后缀与包含匹配；无通配符退化为全等。gin 路径跳过与 gRPC 方法跳过共用。
func MatchWildcard(text, pattern string) bool {
	if pattern == "*" {
		return true
	}

	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		return strings.Contains(text, pattern[1:len(pattern)-1])
	}

	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(text, pattern[1:])
	}

	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(text, pattern[:len(pattern)-1])
	}

	return text == pattern
}
