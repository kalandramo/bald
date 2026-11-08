// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// traceinject.go 是统一链的 trace 头注入原语与通配匹配（方案 S：原
// pkg/middleware/shared 的逻辑退场并入统一链，J2 重复税 final 清偿）。
// gin/gRPC 子包与 shared 兼容壳均以别名指向这里，实现只此一份。

package middleware

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