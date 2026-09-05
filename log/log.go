// Package log 定义 bald 框架级日志契约与全局句柄。
//
// 设计参照：
//   - go-lulu/log：极简接口 + 全局注册 + 零后端默认（nop）
//   - kratos/log：标准库 log/slog 薄封装 + 上下文属性流
//   - onexstack/pkg/otelslog：slog.Handler 桥接 OpenTelemetry Logs（可选后端）
//
// 本包核心（log.go / global.go / nop.go）不依赖任何具体日志库；
// 默认后端为 nop（静默），由用户在 AppKit 装配期经 log.SetLogger 注入具体实现。
package log

import "context"

// Level 表示日志级别，值越大越严重。
type Level int

const (
	// LevelDebug 最详细，用于开发调试。
	LevelDebug Level = iota
	// LevelInfo 常规运行信息。
	LevelInfo
	// LevelWarn 潜在问题，但不影响继续运行。
	LevelWarn
	// LevelError 错误，功能受影响。
	LevelError
)

// Logger 是框架级最小日志契约。
//
// 第一参数为 context.Context，便于后端提取 traceID / request_id 等上下文字段；
// 无 context 时可传 nil。所有方法均要求并发安全。
type Logger interface {
	// Debug 输出 Debug 级日志。
	Debug(ctx context.Context, msg string, args ...any)
	// Info 输出 Info 级日志。
	Info(ctx context.Context, msg string, args ...any)
	// Warn 输出 Warn 级日志。
	Warn(ctx context.Context, msg string, args ...any)
	// Error 输出 Error 级日志。
	Error(ctx context.Context, msg string, args ...any)

	// Enabled 报告后端是否会输出给定级别，用于守卫昂贵参数构造。
	Enabled(level Level) bool

	// With 返回携带给定 key-value 的新 Logger，常用于标记模块/请求。
	// 返回的实例应与原实例相互独立（不可变语义）。
	With(args ...any) Logger
}
