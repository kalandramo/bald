package log

import (
	"context"
	"testing"
)

// recordingLogger 记录包级 helper 的转发目标与方法调用。
type recordingLogger struct {
	stubLogger
	level Level
	withN int
}

func (r *recordingLogger) Debug(context.Context, string, ...any) { r.level = LevelDebug }
func (r *recordingLogger) Info(context.Context, string, ...any)  { r.level = LevelInfo }
func (r *recordingLogger) Warn(context.Context, string, ...any)  { r.level = LevelWarn }
func (r *recordingLogger) Error(context.Context, string, ...any) { r.level = LevelError }
func (r *recordingLogger) Enabled(level Level) bool              { r.level = level; return true }
func (r *recordingLogger) With(args ...any) Logger               { r.withN = len(args); return r }

func TestGlobalHelpersForwardToGlobalLogger(t *testing.T) {
	defer SetLogger(nil)
	rec := &recordingLogger{}
	SetLogger(rec)
	ctx := context.Background()

	Debug(ctx, "d")
	Info(ctx, "i")
	Warn(ctx, "w")
	Error(ctx, "e")
	if rec.level != LevelError {
		t.Fatalf("last forwarded call level = %v, want LevelError", rec.level)
	}

	if !Enabled(LevelWarn) || rec.level != LevelWarn {
		t.Fatal("Enabled should forward to global logger")
	}

	if l := With("k", "v"); l == nil || rec.withN != 2 {
		t.Fatal("With should forward to global logger with args")
	}

	// 未注入（nop 默认）时调用不应 panic 且 Enabled 恒 false。
	SetLogger(nil)
	if Enabled(LevelInfo) {
		t.Fatal("nop default should never be enabled")
	}
	Debug(ctx, "silent")
}
