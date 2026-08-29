package store

import "context"

// Logger 是存储层的日志接口（对齐 pkg/log 的最小子集，避免核心反向依赖框架 log 包）。
// 实现可桥接到 pkg/log.Logger（见下方 FromLog 适配器如需）。
type Logger interface {
	// Error 记录错误级日志。
	Error(ctx context.Context, err error, msg string, kvs ...any)
	// Info 记录信息级日志（可选实现可空转）。
	Info(ctx context.Context, msg string, kvs ...any)
}

// NopLogger 是空转实现，供无日志需求的场景（如测试）使用。
type NopLogger struct{}

func (NopLogger) Error(_ context.Context, _ error, _ string, _ ...any) {}
func (NopLogger) Info(_ context.Context, _ string, _ ...any)           {}

// logAdapter 把外部 *log.Logger 适配为 store.Logger。
// 放在独立文件以避免 pkg/store 直接 import pkg/log（保持零耦合）。
type logAdapter struct {
	l interface {
		Error(ctx context.Context, err error, msg string, kvs ...any)
		Info(ctx context.Context, msg string, kvs ...any)
	}
}

func (a logAdapter) Error(ctx context.Context, err error, msg string, kvs ...any) {
	a.l.Error(ctx, err, msg, kvs...)
}
func (a logAdapter) Info(ctx context.Context, msg string, kvs ...any) {
	a.l.Info(ctx, msg, kvs...)
}

// FromLogger 把任意满足 Error/Info 签名的对象适配为 store.Logger。
// 业务可传入 *log.Logger（pkg/log）或自定义实现。
func FromLogger(l interface {
	Error(ctx context.Context, err error, msg string, kvs ...any)
	Info(ctx context.Context, msg string, kvs ...any)
}) Logger {
	return logAdapter{l: l}
}
