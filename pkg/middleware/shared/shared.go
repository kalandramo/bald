// Package shared 是统一链 trace 注入原语的兼容壳（方案 S：逻辑已退场并入
// pkg/middleware 统一链，见 middleware.InjectTrace/MatchWildcard）。本包不再
// 持有实现，仅以别名转发，维持既有 import 路径稳定；新代码请直接引用
// pkg/middleware。
//
// Deprecated: 实现已并入 pkg/middleware（J2 重复税 final 清偿）。本包仅为
// 兼容别名，随方案 S 批次 4 的旧链移除一并退场。
package shared

import (
	"go.opentelemetry.io/otel/trace"

	"github.com/kalandramo/bald/pkg/middleware"
)

// Standard trace header keys（转发至 middleware）。
const (
	TraceParentHeaderKey = middleware.TraceParentHeaderKey
	TraceIDHeaderKey     = middleware.TraceIDHeaderKey
	RequestIDHeaderKey   = middleware.RequestIDHeaderKey
	TraceStateHeaderKey  = middleware.TraceStateHeaderKey
)

// TraceInjectionMode defines how trace information is injected（转发至 middleware）。
type TraceInjectionMode = middleware.TraceInjectionMode

const (
	InjectW3CTraceContext = middleware.InjectW3CTraceContext
	InjectTraceIDOnly     = middleware.InjectTraceIDOnly
	InjectBoth            = middleware.InjectBoth
	InjectNone            = middleware.InjectNone
)

// InjectTrace 转发至 middleware.InjectTrace。
//
// Deprecated: 用 middleware.InjectTrace。
func InjectTrace(mode TraceInjectionMode, customTraceHeader string, spanCtx trace.SpanContext, set func(key, value string)) {
	middleware.InjectTrace(mode, customTraceHeader, spanCtx, set)
}

// MatchWildcard 转发至 middleware.MatchWildcard。
//
// Deprecated: 用 middleware.MatchWildcard。
func MatchWildcard(text, pattern string) bool {
	return middleware.MatchWildcard(text, pattern)
}