package log

import "context"

// nopLogger 是零成本的静默实现，作为未注入后端时的默认行为。
// 保证 import pkg/log 无任何副作用与输出。
type nopLogger struct{}

func (nopLogger) Debug(context.Context, string, ...any) {}
func (nopLogger) Info(context.Context, string, ...any)  {}
func (nopLogger) Warn(context.Context, string, ...any)  {}
func (nopLogger) Error(context.Context, string, ...any) {}

// Enabled 对 nop 始终返回 false，避免调用方构造无意义参数。
func (nopLogger) Enabled(Level) bool { return false }

// With 返回自身（不可变）。
func (nopLogger) With(...any) Logger { return nopLogger{} }

// NewNop 返回 nop 后端实例：零输出、零依赖、Enabled 恒 false。
// 供装配层（bootstrap NopLoggerProvider）按契约 type=nop 显式选择静默，
// 也适合测试与只需业务指标的场景直接使用。
func NewNop() Logger { return nopLogger{} }
