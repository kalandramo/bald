package log

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

func (h *filterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &filterHandler{next: h.next.WithAttrs(attrs), filter: h.filter}
}

func (h *filterHandler) WithGroup(name string) slog.Handler {
	return &filterHandler{next: h.next.WithGroup(name), filter: h.filter}
}
