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
type ctxKeyTenantID struct{}
type ctxKeyPlatform struct{}

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
	if v, ok := ctx.Value(ctxKeyUsername{}).(string); ok {
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

// WithTenantID 将租户 ID 注入 context（多租户隔离由 pkg/store 在查询时自动读取）。
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, ctxKeyTenantID{}, tenantID)
}

// TenantIDFromContext 从 context 读取租户 ID，缺失返回空串。
func TenantIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyTenantID{}).(string); ok {
		return v
	}
	return ""
}

// WithPlatform 标记当前请求为**平台级身份**（跨租户视图，如平台管理员）。
//
// 语义（务必阅读）：
//   - 这是**显式声明**，绝不基于「TenantID 为空」等隐式条件推断——空租户同时
//     表示「匿名/未认证」与「平台视图」两种相反语义，靠推断会 fail-open。
//   - 平台身份使 pkg/store 的租户隔离对该请求**整体跳过**（跨租户可见）。
//     这不是「权限放大」的替代品——细粒度授权仍由 pkg/authz 负责；本标记只
//     回答「是否按租户切分数据」这一个问题。
//   - 默认 false（fail-closed）：不标记即隔离生效。
func WithPlatform(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyPlatform{}, true)
}

// PlatformFromContext 读取当前请求是否为平台级身份，缺失/未标记返回 false。
//
// 返回 false 时租户隔离生效（fail-closed）——匿名请求与普通租户用户
// 均落到此分支，不会因「无租户值」而被误判为平台视图。
func PlatformFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeyPlatform{}).(bool)
	return v
}
