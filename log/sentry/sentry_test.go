package sentry

import (
	"context"
	"log/slog"
	"testing"

	sentrysdk "github.com/getsentry/sentry-go"

	log "github.com/kalandramo/bald/log"
)

// ctx 属性流必须合并进 Breadcrumb.Data 与 Event.Contexts。
func TestContextAttrsMerged(t *testing.T) {
	s := &sentryLog{}
	ctx := log.ContextWithAttrs(context.Background(),
		slog.String("trace_id", "t-ctx-1"),
	)

	bc := s.breadcrumb(ctx, sentrysdk.LevelDebug, "hello", []any{"user", "alice"})
	if bc.Data["trace_id"] != "t-ctx-1" {
		t.Errorf("breadcrumb Data[trace_id] = %v, want t-ctx-1", bc.Data["trace_id"])
	}
	if bc.Data["user"] != "alice" {
		t.Errorf("breadcrumb Data[user] = %v, want alice", bc.Data["user"])
	}

	evt := s.event(ctx, sentrysdk.LevelError, "boom", []any{"code", 500})
	if got := evt.Contexts["trace_id"]["value"]; got != "t-ctx-1" {
		t.Errorf("event Contexts[trace_id] = %v, want t-ctx-1", got)
	}
	if got := evt.Contexts["code"]["value"]; got != 500 {
		t.Errorf("event Contexts[code] = %v, want 500", got)
	}
}

// 调用参数与 ctx 属性流同名时，参数覆盖（参数在 ctx attrs 之后合入）。
func TestContextAttrsKeyPrecedence(t *testing.T) {
	s := &sentryLog{}
	ctx := log.ContextWithAttrs(context.Background(), slog.String("trace_id", "from-ctx"))

	bc := s.breadcrumb(ctx, sentrysdk.LevelInfo, "hello", []any{"trace_id", "from-args"})
	if bc.Data["trace_id"] != "from-args" {
		t.Errorf("Data[trace_id] = %v, want from-args", bc.Data["trace_id"])
	}
}

// With 携带的 extra 与 ctx 属性流共存。
func TestWithAndContextAttrs(t *testing.T) {
	s := &sentryLog{extra: []any{"module", "api"}}
	ctx := log.ContextWithAttrs(context.Background(), slog.String("trace_id", "t-1"))

	bc := s.breadcrumb(ctx, sentrysdk.LevelDebug, "hello", nil)
	if bc.Data["module"] != "api" {
		t.Errorf("Data[module] = %v, want api", bc.Data["module"])
	}
	if bc.Data["trace_id"] != "t-1" {
		t.Errorf("Data[trace_id] = %v, want t-1", bc.Data["trace_id"])
	}
}

// nil ctx 与空 ctx：无属性流，不应 panic。
func TestNilContextNoAttrs(t *testing.T) {
	s := &sentryLog{}
	s.breadcrumb(nil, sentrysdk.LevelDebug, "nil ctx", []any{"k", "v"})
	evt := s.event(context.Background(), sentrysdk.LevelError, "empty ctx", nil)
	if _, ok := evt.Contexts["k"]; ok {
		t.Error("empty ctx should not introduce attrs")
	}
}
