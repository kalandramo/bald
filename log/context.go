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
	existing := ContextAttrs(ctx)
	if len(existing) == 0 {
		return context.WithValue(ctx, ctxKey{}, attrs)
	}
	merged := make([]slog.Attr, 0, len(existing)+len(attrs))
	merged = append(merged, existing...)
	merged = append(merged, attrs...)
	return context.WithValue(ctx, ctxKey{}, merged)
}

// ContextAttrs 从 ctx 取出 ContextWithAttrs 挂载的属性（无则返回 nil）。
// 导出给适配器层（log/slog 等）在落盘前合并 ctx 属性流；
// 契约层自身不解释属性，仅负责挂载与读取。
func ContextAttrs(ctx context.Context) []slog.Attr {
	if ctx == nil {
		return nil
	}
	if a, ok := ctx.Value(ctxKey{}).([]slog.Attr); ok {
		return a
	}
	return nil
}

// ContextAttrsToArgs 将 ctx 属性流拍平为 key, value, key, value ... 参数序列，
// 便于非 slog 系后端（aliyun/tencent/loki/sentry/charm）以最低成本合并属性：
// 后端在构造日志条目时同步调用，之后即可安全丢弃 ctx（异步 flush 不受影响）。
// 无属性时返回 nil，调用方零开销。
func ContextAttrsToArgs(ctx context.Context) []any {
	attrs := ContextAttrs(ctx)
	if len(attrs) == 0 {
		return nil
	}
	args := make([]any, 0, len(attrs)*2)
	for _, a := range attrs {
		args = append(args, a.Key, a.Value.Any())
	}
	return args
}
