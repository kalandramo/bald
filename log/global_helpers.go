package log

import "context"

// 本文件提供基于全局 Logger 的包级便捷函数，是框架内输出日志的统一入口
// （appkit/中间件/transport/broker/oss 等一律经此调用，不再写 log.GetLogger().Xxx）。
// 全部转发到 SetLogger 注入的当前后端；未注入时为 nop（零输出、零成本）。

// Debug 经由全局 Logger 输出 Debug 级日志。
func Debug(ctx context.Context, msg string, args ...any) {
	GetLogger().Debug(ctx, msg, args...)
}

// Info 经由全局 Logger 输出 Info 级日志。
func Info(ctx context.Context, msg string, args ...any) {
	GetLogger().Info(ctx, msg, args...)
}

// Warn 经由全局 Logger 输出 Warn 级日志。
func Warn(ctx context.Context, msg string, args ...any) {
	GetLogger().Warn(ctx, msg, args...)
}

// Error 经由全局 Logger 输出 Error 级日志。
func Error(ctx context.Context, msg string, args ...any) {
	GetLogger().Error(ctx, msg, args...)
}

// Enabled 报告全局 Logger 是否会输出给定级别，用于守卫昂贵参数构造。
func Enabled(level Level) bool {
	return GetLogger().Enabled(level)
}

// With 返回携带给定 key-value 的全局 Logger 子实例。
func With(args ...any) Logger {
	return GetLogger().With(args...)
}
