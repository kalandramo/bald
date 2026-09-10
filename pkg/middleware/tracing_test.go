package middleware

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// 测试环境未装配全局 TracerProvider，otel.Tracer 即 no-op——正好覆盖兜底分支。
func TestLogTraceIDs_NoOpGeneratesRandom(t *testing.T) {
	tid1, sid1 := LogTraceIDs(context.Background())
	tid2, sid2 := LogTraceIDs(context.Background())

	if tid1 == "00000000000000000000000000000000" || len(tid1) != 32 {
		t.Fatalf("trace_id should be random 32-hex under no-op tracer, got %q", tid1)
	}
	if sid1 == "0000000000000000" || len(sid1) != 16 {
		t.Fatalf("span_id should be random 16-hex under no-op tracer, got %q", sid1)
	}
	if tid1 == tid2 || sid1 == sid2 {
		t.Fatalf("IDs must be unique per call: (%q,%q) vs (%q,%q)", tid1, sid1, tid2, sid2)
	}
}

func TestLogTraceIDs_ValidSpanPassesThrough(t *testing.T) {
	tid := trace.TraceID{1, 2, 3}
	sid := trace.SpanID{4, 5, 6}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	gotTID, gotSID := LogTraceIDs(ctx)
	if gotTID != tid.String() || gotSID != sid.String() {
		t.Fatalf("want (%s,%s), got (%s,%s)", tid, sid, gotTID, gotSID)
	}
}
