package cobramcp

import (
	"context"
	"log/slog"
	"os"

	baldlog "github.com/kalandramo/bald/log"
)

// stderrLogger 是 cobramcp 内建的 bald/log 后端：文本格式、输出到 stderr。
//
// 为什么内建而不是复用 bald/log/bslog：cobramcp 的 CLI 场景（`mcp start` 走
// stdio）中 stdout 是 MCP 协议通道，日志必须写 stderr；内建后端只依赖标准库，
// 不给本模块引入文件轮转等额外依赖。
type stderrLogger struct {
	logger *slog.Logger
}

// newStderrLogger 构造写往 stderr、按 level 过滤的 bald/log 后端。
func newStderrLogger(level baldlog.Level) baldlog.Logger {
	return &stderrLogger{
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: toSlogLevel(level),
		})),
	}
}

func (l *stderrLogger) Debug(ctx context.Context, msg string, args ...any) {
	l.logger.DebugContext(ctx, msg, args...)
}

func (l *stderrLogger) Info(ctx context.Context, msg string, args ...any) {
	l.logger.InfoContext(ctx, msg, args...)
}

func (l *stderrLogger) Warn(ctx context.Context, msg string, args ...any) {
	l.logger.WarnContext(ctx, msg, args...)
}

func (l *stderrLogger) Error(ctx context.Context, msg string, args ...any) {
	l.logger.ErrorContext(ctx, msg, args...)
}

func (l *stderrLogger) Enabled(level baldlog.Level) bool {
	return l.logger.Enabled(context.Background(), toSlogLevel(level))
}

func (l *stderrLogger) With(args ...any) baldlog.Logger {
	return &stderrLogger{logger: l.logger.With(args...)}
}

// installStderrLogger 为 cobramcp 自带的 CLI 子命令安装 stderr 日志后端。
//
// 进程内模型（[NewMCPServer]）不安装任何后端——日志由宿主（appkit / bootstrap）
// 经 bald/log.SetLogger 装配，未装配时为静默 nop。
func installStderrLogger(level baldlog.Level) {
	baldlog.SetLogger(newStderrLogger(level))
}

// toSlogLevel 把 bald/log 级别映射为标准库级别。
func toSlogLevel(level baldlog.Level) slog.Level {
	switch level {
	case baldlog.LevelDebug:
		return slog.LevelDebug
	case baldlog.LevelWarn:
		return slog.LevelWarn
	case baldlog.LevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
