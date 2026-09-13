package log

import (
	"context"
	"log/slog"
)

// filterMask 是命中脱敏 key 的属性值替换掩码（对齐 bslog.FilterKey 的 "***"）。
const filterMask = "***"

// filterLogger 是契约层脱敏装饰器：日志进入后端前，把命中敏感 key 清单的
// 属性值统一掩码。三类来源全覆盖——四级方法的调用参数、With 派生属性、
// ctx 属性流；属性保留不丢弃（只掩值），与 bslog.FilterKey 语义对齐。
type filterLogger struct {
	inner Logger
	keys  map[string]struct{} // 构造后只读，With 派生共享，并发安全
}

// NewFilterLogger 把 l 包装为脱敏装饰器：keys 中列出的属性 key 在任何日志
// 调用（含 With 派生实例、ctx 属性流范围内）中值均替换为 ***。
// keys 为空或 l 为 nil 时原样返回（零开销直通）。
//
// 定位与分工：这是全后端通用的配置驱动脱敏（远程 loki/SLS/CLS/sentry 等
// 可检索、多角色访问的平台恰是外泄风险最高处）；bslog.WithFilter 是 slog
// 后端 handler 层的任意 Attr 变换（代码驱动、更精细，仅 bslog 可消费），
// 两套并存按需选择。
func NewFilterLogger(l Logger, keys ...string) Logger {
	if len(keys) == 0 || l == nil {
		return l
	}
	set := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		set[k] = struct{}{}
	}
	return &filterLogger{inner: l, keys: set}
}

func (f *filterLogger) Debug(ctx context.Context, msg string, args ...any) {
	f.inner.Debug(f.filterCtx(ctx), msg, f.filterArgs(args)...)
}

func (f *filterLogger) Info(ctx context.Context, msg string, args ...any) {
	f.inner.Info(f.filterCtx(ctx), msg, f.filterArgs(args)...)
}

func (f *filterLogger) Warn(ctx context.Context, msg string, args ...any) {
	f.inner.Warn(f.filterCtx(ctx), msg, f.filterArgs(args)...)
}

func (f *filterLogger) Error(ctx context.Context, msg string, args ...any) {
	f.inner.Error(f.filterCtx(ctx), msg, f.filterArgs(args)...)
}

// Enabled 透传：脱敏不改变级别门槛语义。
func (f *filterLogger) Enabled(level Level) bool {
	return f.inner.Enabled(level)
}

// With 在入口过滤后下沉派生，派生实例继续持有 key 集合——后续调用参数
// 仍被过滤，派生链语义与内层不可变派生一致。
func (f *filterLogger) With(args ...any) Logger {
	return &filterLogger{inner: f.inner.With(f.filterArgs(args)...), keys: f.keys}
}

// filterArgs 掩码调用参数中的命中项：kv 对的偶数下标是 key（仅 string 类型
// 参与匹配，非 string key 不命中），奇数下标是 value。未命中时返回原切片，
// 不产生分配。
func (f *filterLogger) filterArgs(args []any) []any {
	hit := false
	for i := 0; i+1 < len(args); i += 2 {
		if k, ok := args[i].(string); ok {
			if _, m := f.keys[k]; m {
				hit = true
				break
			}
		}
	}
	if !hit {
		return args
	}
	filtered := make([]any, len(args))
	copy(filtered, args)
	for i := 0; i+1 < len(filtered); i += 2 {
		if k, ok := filtered[i].(string); ok {
			if _, m := f.keys[k]; m {
				filtered[i+1] = filterMask
			}
		}
	}
	return filtered
}

// filterCtx 把 ctx 属性流中的命中项掩码后整体重挂。不走 ContextWithAttrs
// （merge 语义会保留未过滤原属性），直接以包私有 ctxKey 覆盖——本包内访问
// 私有键是合法特权。ctx 为 nil 或无属性 / 未命中时原样返回。
func (f *filterLogger) filterCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	attrs := ContextAttrs(ctx)
	if len(attrs) == 0 {
		return ctx
	}
	hit := false
	for _, a := range attrs {
		if _, m := f.keys[a.Key]; m {
			hit = true
			break
		}
	}
	if !hit {
		return ctx
	}
	filtered := make([]slog.Attr, len(attrs))
	copy(filtered, attrs)
	for i, a := range filtered {
		if _, m := f.keys[a.Key]; m {
			filtered[i].Value = slog.StringValue(filterMask)
		}
	}
	return context.WithValue(ctx, ctxKey{}, filtered)
}
