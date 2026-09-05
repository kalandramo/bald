package slogadapter

import (
	"context"
	"log/slog"
)

// Filter 对单条属性做转换，返回零值（slog.StringValue("")）即可丢弃该属性。
type Filter func(slog.Attr) slog.Attr

// FilterKey 返回一个脱敏过滤器：匹配给定 key 的属性值被替换为 "***"。
// 典型用法：WithFilter(FilterKey("password"))。
func FilterKey(key string) Filter {
	return func(a slog.Attr) slog.Attr {
		if a.Key == key {
			a.Value = slog.StringValue("***")
		}
		return a
	}
}

// filterHandler 是 slog.Handler 装饰器，按 Filter 改写每条记录的属性。
type filterHandler struct {
	next   slog.Handler
	filter Filter
}

func (h *filterHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *filterHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.filter(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

// WithAttrs 对将要固化的属性先过 filter 再下沉——否则这些属性绕过本层 Handle
// （slog 把 WithAttrs 的属性由内层 handler 在 Handle 阶段直接合并），导致
// FilterKey 脱敏对 logger.With("password", ...) / 构造期 WithAttrs Option 失效。
func (h *filterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	filtered := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		filtered = append(filtered, h.filter(a))
	}
	return &filterHandler{next: h.next.WithAttrs(filtered), filter: h.filter}
}

func (h *filterHandler) WithGroup(name string) slog.Handler {
	return &filterHandler{next: h.next.WithGroup(name), filter: h.filter}
}
