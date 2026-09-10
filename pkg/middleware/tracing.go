// Package middleware 提供 gin/gRPC 协议中间件（middleware/gin、middleware/grpc）
// 的共享工具。中间件实现各自在协议子包，仅跨协议复用的逻辑落在本包。
package middleware

import (
	"context"
	"crypto/rand"

	"go.opentelemetry.io/otel/trace"
)

// LogTraceIDs 返回用于日志关联的 trace_id/span_id。
//
// SpanContext 有效（已装配全局 TracerProvider，真实 tracer 在跑）时透传真实值；
// 无效（未装配，otel.Tracer 为 no-op——见各 observability 中间件的零配置约定）时
// 生成随机 ID，仅作日志请求关联：不导出 span、不注入协议头，零配置语义不变。
// 动机：no-op 下 SpanContext 恒全零，日志 trace_id/span_id 固定为 32/16 个 0，
// 无法按请求关联日志；兜底随机 ID 让「零配置也有可关联的日志链路」。
func LogTraceIDs(ctx context.Context) (traceID, spanID string) {
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() {
		return sc.TraceID().String(), sc.SpanID().String()
	}

	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败极罕见；返回空串比全零诚实（全零会被误读为有效占位）。
		return "", ""
	}
	var tid trace.TraceID
	var sid trace.SpanID
	copy(tid[:], b[:16])
	copy(sid[:], b[16:24])
	return tid.String(), sid.String()
}
