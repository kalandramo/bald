package grpc

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/kalandramo/bald/pkg/contextx"
)

// spanKindRecorder 拦截 Start 的 SpanKind（纯 otel API，不引入 sdk）。
// 嵌入 trace.Tracer 携带未导出标记方法 tracer()（只调 Start，安全）。
type spanKindRecorder struct {
	trace.Tracer
	kinds []trace.SpanKind
}

func (r *spanKindRecorder) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	cfg := trace.NewSpanStartConfig(opts...)
	r.kinds = append(r.kinds, cfg.SpanKind())
	return ctx, trace.SpanFromContext(ctx)
}

// fakeServerStream 最小 grpc.ServerStream 桩（仅满足 Context/收发声明）。
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *fakeServerStream) Context() context.Context { return s.ctx }
func (s *fakeServerStream) SendMsg(any) error        { return nil }
func (s *fakeServerStream) RecvMsg(any) error        { return nil }

// TestObservability_SpanKindServer：inbound gRPC span（unary 与 stream）必须
// SpanKindServer（OTel 语义；spanmetrics 服务端聚合与服务拓扑图依赖该值——
// 回归：曾缺省 internal 被漏计）。同包测试直接替换包级 tracer。
func TestObservability_SpanKindServer(t *testing.T) {
	rec := &spanKindRecorder{}
	prev := tracer
	tracer = rec
	defer func() { tracer = prev }()

	// unary
	un := UnaryObservability()
	_, err := un(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/svc/Get"},
		func(ctx context.Context, req any) (any, error) { return nil, nil })
	if err != nil {
		t.Fatalf("unary handler error: %v", err)
	}

	// stream
	st := StreamObservability()
	_ = st(nil, &fakeServerStream{ctx: metadata.NewIncomingContext(context.Background(), nil)},
		&grpc.StreamServerInfo{FullMethod: "/svc/Watch"},
		func(srv any, ss grpc.ServerStream) error { return nil })

	if len(rec.kinds) != 2 {
		t.Fatalf("expected 2 spans (unary+stream), got %d", len(rec.kinds))
	}
	for i, k := range rec.kinds {
		if k != trace.SpanKindServer {
			t.Errorf("span[%d] kind = %v, want SpanKindServer", i, k)
		}
	}
}

// TestObservability_TraceIDInContext：unary 与 stream 的 handler ctx 内
// contextx.TraceIDFromContext 必须非空非全零（审计-链路关联修复防回归
// 锚点——audit/authn/crudbridge 的 TraceIDFromContext 消费依赖）。
// stream 用例刻意不带 incoming metadata：回归此前「仅含 metadata 才包装
// 流」导致整类流的 enriched ctx（trace_id/日志属性）丢失。
func TestObservability_TraceIDInContext(t *testing.T) {
	// unary
	var unaryTID string
	un := UnaryObservability()
	_, err := un(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/svc/Get"},
		func(ctx context.Context, req any) (any, error) {
			unaryTID = contextx.TraceIDFromContext(ctx)
			return nil, nil
		})
	if err != nil {
		t.Fatalf("unary handler error: %v", err)
	}
	if unaryTID == "" || unaryTID == "00000000000000000000000000000000" {
		t.Errorf("unary handler ctx TraceID 应为兜底随机 ID, got %q", unaryTID)
	}

	// stream（无 incoming metadata——包装修正的回归锚点）
	var streamTID string
	st := StreamObservability()
	_ = st(nil, &fakeServerStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/svc/Watch"},
		func(srv any, ss grpc.ServerStream) error {
			streamTID = contextx.TraceIDFromContext(ss.Context())
			return nil
		})
	if streamTID == "" || streamTID == "00000000000000000000000000000000" {
		t.Errorf("stream handler ctx TraceID 应为兜底随机 ID（无 metadata 流也必须包装）, got %q", streamTID)
	}
}
