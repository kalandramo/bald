// Package contextx 提供 bald 框架在 context 中存取请求级元信息的便捷函数。
//
// 设计对齐 onexstack 的 internal/pkg/contextx，但作为框架级共享工具独立成包，
// 供 pkg/middleware 的 gin/grpc 子包与业务 handler 共同引用，避免重复的私有键定义。
package contextx

import "context"

type ctxKeyUserID struct{}
type ctxKeyUsername struct{}
type ctxKeyTraceID struct{}
type ctxKeyRequestID struct{}

// WithUserID 将用户 ID 注入 context。
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ctxKeyUserID{}, userID)
}

// UserIDFromContext 从 context 读取用户 ID，缺失返回空串。
func UserIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyUserID{}).(string); ok {
		return v
	}
	return ""
}

// WithRequestID 将请求 ID 注入 context。
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID{}, requestID)
}

// RequestIDFromContext 从 context 读取请求 ID，缺失返回空串。
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID{}).(string); ok {
		return v
	}
	return ""
}

// WithUsername 将用户名注入 context。
func WithUsername(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, ctxKeyUsername{}, username)
}

// UsernameFromContext 从 context 读取用户名，缺失返回空串。
func UsernameFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyUsername{}).(  string); ok {
		return v
	}
	return ""
}

// WithTraceID 将 TraceID 注入 context。
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, ctxKeyTraceID{}, traceID)
}

// TraceIDFromContext 从 context 读取 TraceID，缺失返回空串。
func TraceIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyTraceID{}).(string); ok {
		return v
	}
	return ""
}
