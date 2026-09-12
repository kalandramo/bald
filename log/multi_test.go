package log_test

// MultiLogger 的集成测试用外部测试包（log_test）：需要引入 slogadapter 构造
// 真实后端验证广播语义。外部测试包是独立包，不构成 log→slog→log 循环，
// 同时不污染契约包自身的依赖图（契约层 in-package 测试仍只用 stub，见 log_test.go）。

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/kalandramo/bald/log"
	slogadapter "github.com/kalandramo/bald/log/slog"
)

// newSink 用 slog 后端构造一个写到 w 的 Logger，级别由 lvl 控制。
func newSink(w io.Writer, lvl slog.Level) log.Logger {
	return slogadapter.NewSlogLogger(slogadapter.NewOptions(),
		slogadapter.WithHandler(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})))
}

func TestMultiLogger_Broadcasts(t *testing.T) {
	a := &bytes.Buffer{}
	b := &bytes.Buffer{}
	la := newSink(a, slog.LevelInfo)
	lb := newSink(b, slog.LevelInfo)

	m := log.NewMultiLogger(la, lb)
	m.Info(context.Background(), "fanout", "k", "v")

	for _, buf := range []*bytes.Buffer{a, b} {
		if !bytes.Contains(buf.Bytes(), []byte("fanout")) {
			t.Fatalf("every sink should receive the record, got: %s", buf.String())
		}
	}
}

func TestMultiLogger_EnabledAny(t *testing.T) {
	quiet := newSink(io.Discard, slog.LevelError)
	loose := newSink(io.Discard, slog.LevelDebug)

	m := log.NewMultiLogger(quiet, loose)
	if !m.Enabled(log.LevelDebug) {
		t.Fatal("any enabled sink should enable the level")
	}
	m2 := log.NewMultiLogger(quiet)
	if m2.Enabled(log.LevelDebug) {
		t.Fatal("no enabled sink should not enable the level")
	}
}

func TestMultiLogger_WithCombines(t *testing.T) {
	a := &bytes.Buffer{}
	b := &bytes.Buffer{}
	m := log.NewMultiLogger(
		newSink(a, slog.LevelInfo),
		newSink(b, slog.LevelInfo),
	)

	m.With("svc", "demo").Info(context.Background(), "tagged")
	for _, buf := range []*bytes.Buffer{a, b} {
		if !bytes.Contains(buf.Bytes(), []byte("demo")) {
			t.Fatalf("With attrs should reach every sink: %s", buf.String())
		}
	}
}

func TestMultiLogger_EmptyIsInert(t *testing.T) {
	m := log.NewMultiLogger()
	if m.Enabled(log.LevelInfo) {
		t.Fatal("empty multi logger should not be enabled")
	}
	m.Info(context.Background(), "ignored") // 不 panic
	if m.With("k", "v") == nil {
		t.Fatal("With should return non-nil")
	}
}

// TestMultiLogger_NilSinkSkipped 验证 nil 子 Logger 被过滤（对齐原 NewMultiLogger 语义）。
func TestMultiLogger_NilSinkSkipped(t *testing.T) {
	m := log.NewMultiLogger(nil, newSink(io.Discard, slog.LevelDebug))
	if !m.Enabled(log.LevelInfo) {
		t.Fatal("nil sink should be skipped, remaining sink still enables")
	}
}
