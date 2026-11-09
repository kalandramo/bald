package log

import (
	"context"
	"log/slog"
	"testing"
)

// TestFilterLoggerLevels 覆盖 filterLogger 的 Debug/Warn/Error 入口。
func TestFilterLoggerLevels(t *testing.T) {
	sink := newFilterSink()
	fl := NewFilterLogger(sink, "password")
	ctx := context.Background()

	fl.Debug(ctx, "d", "password", "d-secret")
	fl.Warn(ctx, "w", "password", "w-secret")
	fl.Error(ctx, "e", "password", "e-secret")

	if len(*sink.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(*sink.calls))
	}
	for i, want := range []string{"debug", "warn", "error"} {
		call := (*sink.calls)[i]
		if call.level != want {
			t.Errorf("call %d level = %q, want %q", i, call.level, want)
		}
		if !argsContains(call.args, "password", "***") {
			t.Errorf("call %d args not redacted: %v", i, call.args)
		}
	}
}

// TestFilterLoggerCtxAttrsNoHit 覆盖 ctx 有属性但无命中 key 时原样返回。
func TestFilterLoggerCtxAttrsNoHit(t *testing.T) {
	sink := newFilterSink()
	fl := NewFilterLogger(sink, "password")

	ctx := ContextWithAttrs(context.Background(), slog.String("trace_id", "t-1"))
	fl.Info(ctx, "req", "user", "alice")

	args := lastArgs(*sink.calls)
	if !argsContains(args, "trace_id", "t-1") {
		t.Errorf("ctx attrs should pass through: %v", args)
	}
	if !argsContains(args, "user", "alice") {
		t.Errorf("call args should pass through: %v", args)
	}
}

// TestNopLogger 覆盖静默后端的全部方法与派生语义。
func TestNopLogger(t *testing.T) {
	var l Logger = nopLogger{}
	ctx := context.Background()
	l.Debug(ctx, "d")
	l.Info(ctx, "i")
	l.Warn(ctx, "w")
	l.Error(ctx, "e")

	if l.Enabled(LevelError) {
		t.Error("nop must always report disabled")
	}
	if _, ok := l.With("k", "v").(nopLogger); !ok {
		t.Errorf("nop With must return nopLogger, got %T", l.With("k", "v"))
	}
}
