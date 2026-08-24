package log

import (
	"context"
	"log/slog"
)

// ctxKey 是上下文属性流的私有键。
type ctxKey struct{}

// ContextWithAttrs 将结构化属性挂载到 ctx，使该 ctx 范围内的所有日志自动携带。
// 用法对齐 kratos/log 的 ContextWithAttrs。
func ContextWithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	existing := extractAttrs(ctx)
	if len(existing) == 0 {
		return context.WithValue(ctx, ctxKey{}, attrs)
	}
	merged := make([]slog.Attr, 0, len(existing)+len(attrs))
	merged = append(merged, existing...)
	merged = append(merged, attrs...)
	return context.WithValue(ctx, ctxKey{}, merged)
}

// extractAttrs 从 ctx 取出挂载的属性（无则返回 nil）。
func extractAttrs(ctx context.Context) []slog.Attr {
	if ctx == nil {
		return nil
	}
	if a, ok := ctx.Value(ctxKey{}).([]slog.Attr); ok {
		return a
	}
	return nil
}
