package log

import (
	"context"
	"log/slog"
	"testing"
)

func TestContextWithAttrsAndRead(t *testing.T) {
	ctx := ContextWithAttrs(context.Background(),
		slog.String("trace_id", "t-1"),
		slog.Int("uid", 42),
	)

	attrs := ContextAttrs(ctx)
	if len(attrs) != 2 {
		t.Fatalf("want 2 attrs, got %d", len(attrs))
	}
	if attrs[0].Key != "trace_id" || attrs[0].Value.String() != "t-1" {
		t.Errorf("attrs[0] = %v, want trace_id=t-1", attrs[0])
	}
	if attrs[1].Key != "uid" || attrs[1].Value.String() != "42" {
		t.Errorf("attrs[1] = %v, want uid=42", attrs[1])
	}
}

func TestContextAttrsMergesAcrossCalls(t *testing.T) {
	ctx := ContextWithAttrs(context.Background(), slog.String("a", "1"))
	ctx = ContextWithAttrs(ctx, slog.String("b", "2"))

	attrs := ContextAttrs(ctx)
	if len(attrs) != 2 || attrs[0].Key != "a" || attrs[1].Key != "b" {
		t.Fatalf("want merged [a, b], got %v", attrs)
	}
}

func TestContextAttrsNilCases(t *testing.T) {
	if got := ContextAttrs(nil); got != nil {
		t.Errorf("ContextAttrs(nil) = %v, want nil", got)
	}
	if got := ContextAttrs(context.Background()); got != nil {
		t.Errorf("ContextAttrs(empty) = %v, want nil", got)
	}
}

func TestContextAttrsToArgs(t *testing.T) {
	if got := ContextAttrsToArgs(nil); got != nil {
		t.Errorf("ContextAttrsToArgs(nil) = %v, want nil", got)
	}
	if got := ContextAttrsToArgs(context.Background()); got != nil {
		t.Errorf("ContextAttrsToArgs(empty) = %v, want nil", got)
	}

	ctx := ContextWithAttrs(context.Background(),
		slog.String("request_id", "r-9"),
		slog.Int64("retries", 3),
	)
	args := ContextAttrsToArgs(ctx)
	want := []any{"request_id", "r-9", "retries", int64(3)}
	if len(args) != len(want) {
		t.Fatalf("want %d args, got %d (%v)", len(want), len(args), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %v, want %v", i, args[i], want[i])
		}
	}
}
