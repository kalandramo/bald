package charm

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	charmlog "github.com/charmbracelet/log"

	log "github.com/kalandramo/bald/log"
)

// newCaptureLogger 构造输出到内存 buffer 的 charm Logger，便于断言输出内容。
func newCaptureLogger() (*Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	l := charmlog.New(&buf)
	l.SetLevel(charmlog.DebugLevel)
	l.SetReportTimestamp(false)
	return NewLoggerWith(l), &buf
}

// ctx 属性流必须随日志输出。
func TestContextAttrsInOutput(t *testing.T) {
	l, buf := newCaptureLogger()

	ctx := log.ContextWithAttrs(context.Background(),
		slog.String("trace_id", "t-ctx-1"),
	)
	l.Info(ctx, "hello", "user", "alice")

	out := buf.String()
	if !strings.Contains(out, "trace_id=t-ctx-1") {
		t.Errorf("output missing ctx attr trace_id: %s", out)
	}
	if !strings.Contains(out, "user=alice") {
		t.Errorf("output missing user=alice: %s", out)
	}
}

// 调用参数与 ctx 属性流同名时，参数覆盖。
func TestContextAttrsKeyPrecedence(t *testing.T) {
	l, buf := newCaptureLogger()

	ctx := log.ContextWithAttrs(context.Background(), slog.String("trace_id", "from-ctx"))
	l.Info(ctx, "hello", "trace_id", "from-args")

	if !strings.Contains(buf.String(), "trace_id=from-args") {
		t.Errorf("call args should override ctx attr: %s", buf.String())
	}
}

// With 携带的属性与 ctx 属性流共存。
func TestWithAndContextAttrs(t *testing.T) {
	l, buf := newCaptureLogger()

	ctx := log.ContextWithAttrs(context.Background(), slog.String("trace_id", "t-1"))
	l.With("module", "api").Info(ctx, "hello")

	out := buf.String()
	if !strings.Contains(out, "module=api") {
		t.Errorf("output missing module=api: %s", out)
	}
	if !strings.Contains(out, "trace_id=t-1") {
		t.Errorf("output missing trace_id=t-1: %s", out)
	}
}

// nil ctx 与空 ctx：不应 panic，正常输出调用参数。
func TestNilContextNoAttrs(t *testing.T) {
	l, buf := newCaptureLogger()

	l.Info(nil, "nil ctx", "k", "v")
	l.Info(context.Background(), "empty ctx")

	out := buf.String()
	if !strings.Contains(out, "k=v") {
		t.Errorf("nil ctx should still log args: %s", out)
	}
	if strings.Contains(out, "trace_id") {
		t.Errorf("no ctx attrs expected: %s", out)
	}
}
