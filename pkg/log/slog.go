package log

import (
	"context"
	"io"
	"log/slog"
	"os"

	lumberjack "gopkg.in/natefinch/lumberjack.v2"

	"golang.org/x/sync/errgroup"
)

// 使用本地别名，避免与包名 log 混淆（标准库 log/slog 的 Level 与包的 Level 区分）。
const (
	slogLevelDebug = slog.LevelDebug
	slogLevelInfo  = slog.LevelInfo
	slogLevelWarn  = slog.LevelWarn
	slogLevelError = slog.LevelError
)

// config 收集 NewSlogLogger 的可选项。
type config struct {
	handler slog.Handler  // 外部注入的 handler（如 OTel），优先级最高
	filters []Filter      // 脱敏/过滤装饰器
	attrs   []slog.Attr   // 附加固定属性
	writer  io.Writer     // 仅测试用，覆盖 OutputPaths 的写入目标
}

// Option 用于定制 NewSlogLogger 的行为。
type Option func(*config)

// WithHandler 注入自定义 slog.Handler，覆盖默认构造（console/json）的 handler。
// 这是接入 OpenTelemetry、Lumberjack 等后端的统一扩展点。
func WithHandler(h slog.Handler) Option {
	return func(c *config) { c.handler = h }
}

// WithFilter 追加一个字段过滤/脱敏装饰器，按注册顺序依次生效。
func WithFilter(f Filter) Option {
	return func(c *config) { c.filters = append(c.filters, f) }
}

// WithAttrs 为后端追加固定的结构化属性（如 service.name / version）。
func WithAttrs(attrs ...slog.Attr) Option {
	return func(c *config) { c.attrs = append(c.attrs, attrs...) }
}

// withWriter 仅用于测试：覆盖输出目标。
func withWriter(w io.Writer) Option {
	return func(c *config) { c.writer = w }
}

// NewSlogLogger 基于标准库 log/slog 构造一个开箱即用的 Logger 后端。
// 当 opts 未提供 WithHandler 时，按 Options 的 Level/Format/OutputPaths 构造
// console 或 json handler 并输出到 stdout（或文件）。
func NewSlogLogger(o *Options, opts ...Option) Logger {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}

	var h slog.Handler
	if cfg.handler != nil {
		h = cfg.handler
	} else {
		level, err := parseLevel(o.Level)
		if err != nil {
			// 配置非法时回退 info，避免崩溃（不采用 onexstack 的 panic 做法）。
			level = slogLevelInfo
		}
		w := cfg.writer
		if w == nil {
			w = openWriter(o)
		}
		handlerOpts := &slog.HandlerOptions{Level: level}
		if o.Format == "json" {
			h = slog.NewJSONHandler(w, handlerOpts)
		} else {
			h = slog.NewTextHandler(w, handlerOpts)
		}
	}

	// 应用脱敏/过滤装饰器。
	for _, f := range cfg.filters {
		h = &filterHandler{next: h, filter: f}
	}
	// 应用固定属性。
	if len(cfg.attrs) > 0 {
		h = h.WithAttrs(cfg.attrs)
	}

	return &slogLogger{slog: slog.New(h)}
}

// openWriter 按 Options 的 OutputPaths 与 Rotate 选择写入目标。多目标时全部写入；
// 任一路径非法不影响其余目标（首个文件打开失败时回退 stdout）。
// 当某路径为文件路径且 Rotate.Enabled 时，用 lumberjack 接管轮转（切割/清理/gzip）。
func openWriter(o *Options) io.Writer {
	var ws []io.Writer
	for _, p := range o.OutputPaths {
		switch p {
		case "stdout":
			ws = append(ws, os.Stdout)
		case "stderr":
			ws = append(ws, os.Stderr)
		default:
			// 文件路径：启用轮转时用 lumberjack，否则 os.OpenFile 直写。
			if o.Rotate != nil && o.Rotate.Enabled {
				ws = append(ws, newRotateWriter(p, o.Rotate))
				continue
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				// 打开失败回退 stdout，保证可观测性不丢。
				ws = append(ws, os.Stdout)
				continue
			}
			ws = append(ws, f)
		}
	}
	if len(ws) == 0 {
		return os.Stdout
	}
	if len(ws) == 1 {
		return ws[0]
	}
	// 多目标：并发写入，任一失败不影响其余。
	return multiWriter(ws)
}

// newRotateWriter 基于 lumberjack 构造支持轮转的文件 writer。
// lumberjack.Logger 实现 io.Writer，可被 slog 的 Handler 直接消费；
// 切割/备份/清理/压缩由 lumberjack 在写入时按需触发。
func newRotateWriter(path string, r *RotateOptions) io.Writer {
	return &lumberjack.Logger{
		Filename:   path,
		MaxSize:    r.MaxSize,    // MB
		MaxBackups: r.MaxBackups,
		MaxAge:     r.MaxAge,     // 天
		Compress:   r.Compress,
	}
}

// multiWriter 向多个 writer 复制写入。
func multiWriter(ws []io.Writer) io.Writer {
	return &multiWriterImpl{ws: ws}
}

type multiWriterImpl struct{ ws []io.Writer }

func (m *multiWriterImpl) Write(p []byte) (int, error) {
	var g errgroup.Group
	for _, w := range m.ws {
		w := w
		g.Go(func() error {
			_, err := w.Write(p)
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return 0, err
	}
	return len(p), nil
}

// slogLogger 是基于 log/slog 的 Logger 实现。
type slogLogger struct {
	slog *slog.Logger
}

func (l *slogLogger) Debug(ctx context.Context, msg string, args ...any) {
	l.log(ctx, slogLevelDebug, msg, args...)
}
func (l *slogLogger) Info(ctx context.Context, msg string, args ...any) {
	l.log(ctx, slogLevelInfo, msg, args...)
}
func (l *slogLogger) Warn(ctx context.Context, msg string, args ...any) {
	l.log(ctx, slogLevelWarn, msg, args...)
}
func (l *slogLogger) Error(ctx context.Context, msg string, args ...any) {
	l.log(ctx, slogLevelError, msg, args...)
}

func (l *slogLogger) Enabled(level Level) bool {
	return l.slog.Enabled(context.Background(), toSlogLevel(level))
}

func (l *slogLogger) With(args ...any) Logger {
	return &slogLogger{slog: l.slog.With(args...)}
}

// log 合并上下文属性（ContextWithAttrs）后委派给 slog。
func (l *slogLogger) log(ctx context.Context, level slog.Level, msg string, args ...any) {
	if a := extractAttrs(ctx); len(a) > 0 {
		merged := make([]any, 0, len(a)*2+len(args))
		for _, at := range a {
			merged = append(merged, at.Key, at.Value)
		}
		merged = append(merged, args...)
		l.slog.Log(ctx, level, msg, merged...)
		return
	}
	l.slog.Log(ctx, level, msg, args...)
}

// toSlogLevel 将包的 Level 映射为 slog.Level。
func toSlogLevel(l Level) slog.Level {
	switch l {
	case LevelDebug:
		return slogLevelDebug
	case LevelWarn:
		return slogLevelWarn
	case LevelError:
		return slogLevelError
	default:
		return slogLevelInfo
	}
}
