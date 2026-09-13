package charm

import (
	"context"
	"fmt"
	"os"
	"strconv"

	charmlog "github.com/charmbracelet/log"

	log "github.com/kalandramo/bald/log"
)

// Compile-time assertion: Logger implements log.Logger（bald 契约）.
var _ log.Logger = (*Logger)(nil)

// Logger 是基于 Charmbracelet log 的日志适配器。
//
// charm/log 提供色彩丰富、人类友好的终端日志输出，支持日志级别、
// 结构化键值对、调用栈报告等功能。非常适合本地开发和调试场景。
//
// Example:
//
//	logger := charm.NewLogger()
//	// 带颜色输出的控制台日志：
//	logger.Info(ctx, "server started", "port", 8080)
type Logger struct {
	log *charmlog.Logger
}

// NewLogger 创建一个带有默认配置的 charm 日志记录器。
// 默认输出到 stderr，INFO 级别，彩色输出。
func NewLogger() *Logger {
	l := charmlog.New(os.Stderr)
	l.SetLevel(charmlog.InfoLevel)
	l.SetReportCaller(false)
	l.SetReportTimestamp(true)
	return &Logger{log: l}
}

// NewLoggerWith 使用给定的 charm charmlog.Logger 创建适配器。
func NewLoggerWith(l *charmlog.Logger) *Logger {
	if l == nil {
		return NewLogger()
	}
	return &Logger{log: l}
}

// Debug 输出 DEBUG 级别日志。
func (l *Logger) Debug(ctx context.Context, msg string, keyvals ...any) {
	l.log.Debug(msg, withContextAttrs(ctx, keyvals)...)
}

// Info 输出 INFO 级别日志。
func (l *Logger) Info(ctx context.Context, msg string, keyvals ...any) {
	l.log.Info(msg, withContextAttrs(ctx, keyvals)...)
}

// Warn 输出 WARN 级别日志。
func (l *Logger) Warn(ctx context.Context, msg string, keyvals ...any) {
	l.log.Warn(msg, withContextAttrs(ctx, keyvals)...)
}

// Error 输出 ERROR 级别日志。
func (l *Logger) Error(ctx context.Context, msg string, keyvals ...any) {
	l.log.Error(msg, withContextAttrs(ctx, keyvals)...)
}

// withContextAttrs 把 ctx 属性流（log.ContextWithAttrs）拍平并前置于调用参数，
// 同名 key 时调用参数覆盖 ctx 属性（与 bslog 语义一致）。
func withContextAttrs(ctx context.Context, keyvals []any) []any {
	attrs := log.ContextAttrsToArgs(ctx)
	if len(attrs) == 0 {
		return keyvals
	}
	merged := make([]any, 0, len(attrs)+len(keyvals))
	merged = append(merged, attrs...)
	merged = append(merged, keyvals...)
	return merged
}

// With 返回附加了指定 key-value 对的新 Logger 实例。
// 使用 charm log 原生的 With() 方法。
func (l *Logger) With(keyvals ...any) log.Logger {
	return &Logger{log: l.log.With(keyvals...)}
}

// Enabled 报告给定级别是否会被输出。
func (l *Logger) Enabled(level log.Level) bool {
	return levelToCharm(level) >= l.log.GetLevel()
}

// Close 是一个空操作。charm log 写入到 io.Writer，无需显式关闭。
func (l *Logger) Close() error {
	return nil
}

// levelToCharm 将契约 Level 映射为 charmlog.Level。
func levelToCharm(level log.Level) charmlog.Level {
	switch level {
	case log.LevelDebug:
		return charmlog.DebugLevel
	case log.LevelInfo:
		return charmlog.InfoLevel
	case log.LevelWarn:
		return charmlog.WarnLevel
	case log.LevelError:
		return charmlog.ErrorLevel
	default:
		return charmlog.InfoLevel
	}
}

// String 返回当前 Logger 的可读描述。
func (l *Logger) String() string {
	return "charm.Logger{level=" + strconv.Itoa(int(l.log.GetLevel())) + ", prefix=" + fmt.Sprint(l.log.GetPrefix()) + "}"
}
