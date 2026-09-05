package log

import "context"

// MultiLogger 将多个 Logger 组合为一个：每条日志广播到全部后端，
// 用于「多输出源」场景（本地文件 + 远程采集同时写）。
//
// 语义：
//   - Debug/Info/Warn/Error：顺序广播到所有子 Logger（单个失败不影响其余）；
//   - Enabled：任一子 Logger 启用该级别即启用（保守放行，避免误拦）；
//   - With：对每个子 Logger 分别 With 后组合为新 MultiLogger。
//
// 空参数时返回的实例广播无目标（等价 nop），Enabled 恒为 false。
// 仅依赖契约本身（纯 Logger 装饰器），故放在契约层顶层，
// 对应 transport 顶层 Serve 的通用设施位置；对应 go-wind-plugins/log 的 MultiLogger。
type MultiLogger struct {
	loggers []Logger
}

// 编译期断言：MultiLogger 实现契约 Logger。
var _ Logger = (*MultiLogger)(nil)

// NewMultiLogger 组合多个 Logger 为一个广播 Logger。
func NewMultiLogger(loggers ...Logger) Logger {
	ls := make([]Logger, 0, len(loggers))
	for _, l := range loggers {
		if l != nil {
			ls = append(ls, l)
		}
	}
	return &MultiLogger{loggers: ls}
}

func (m *MultiLogger) Debug(ctx context.Context, msg string, args ...any) {
	for _, l := range m.loggers {
		l.Debug(ctx, msg, args...)
	}
}

func (m *MultiLogger) Info(ctx context.Context, msg string, args ...any) {
	for _, l := range m.loggers {
		l.Info(ctx, msg, args...)
	}
}

func (m *MultiLogger) Warn(ctx context.Context, msg string, args ...any) {
	for _, l := range m.loggers {
		l.Warn(ctx, msg, args...)
	}
}

func (m *MultiLogger) Error(ctx context.Context, msg string, args ...any) {
	for _, l := range m.loggers {
		l.Error(ctx, msg, args...)
	}
}

func (m *MultiLogger) Enabled(level Level) bool {
	for _, l := range m.loggers {
		if l.Enabled(level) {
			return true
		}
	}
	return false
}

func (m *MultiLogger) With(args ...any) Logger {
	ls := make([]Logger, 0, len(m.loggers))
	for _, l := range m.loggers {
		ls = append(ls, l.With(args...))
	}
	return &MultiLogger{loggers: ls}
}
