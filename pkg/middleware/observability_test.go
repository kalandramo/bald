// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/contextx"
)

// obsSpanRecorder 拦截 Start 的 SpanKind（纯 otel API，不引入 sdk）。嵌入
// trace.Tracer 携带未导出标记方法 tracer()（只调 Start，安全）。
type obsSpanRecorder struct {
	trace.Tracer
	kinds []trace.SpanKind
}

func (r *obsSpanRecorder) Start(ctx context.Context, _ string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	cfg := trace.NewSpanStartConfig(opts...)
	r.kinds = append(r.kinds, cfg.SpanKind())
	return ctx, trace.SpanFromContext(ctx)
}

func ctxTraceID(ctx context.Context) string { return contextx.TraceIDFromContext(ctx) }

func attrTraceID(ctx context.Context) string {
	for _, a := range log.ContextAttrs(ctx) {
		if a.Key == "trace_id" {
			return a.Value.String()
		}
	}
	return ""
}

// TestObservabilityUnified_HTTP：经 GinHandler 桥，HTTP 面起 SpanKindServer，
// trace_id 同源进 contextx 与日志属性流（no-op 兜底随机 ID）。
func TestObservabilityUnified_HTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := &obsSpanRecorder{}
	r := gin.New()
	r.Use(GinHandler(Observability(ObservabilityWithTracer(rec))))

	var ctxTID, attrTID string
	r.GET("/v1/ping", func(c *gin.Context) {
		ctxTID = ctxTraceID(c.Request.Context())
		attrTID = attrTraceID(c.Request.Context())
		c.Status(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/ping", nil))

	if len(rec.kinds) != 1 || rec.kinds[0] != trace.SpanKindServer {
		t.Fatalf("HTTP span kinds = %v, want exactly one SpanKindServer", rec.kinds)
	}
	if ctxTID == "" || ctxTID == "00000000000000000000000000000000" {
		t.Errorf("contextx trace_id 应非空非全零, got %q", ctxTID)
	}
	if ctxTID != attrTID {
		t.Errorf("contextx %q 应与日志属性 %q 同源同值", ctxTID, attrTID)
	}
}

// TestObservabilityUnified_GRPCUnary：经 UnaryServerInterceptor 桥，unary 面起
// SpanKindServer，handler ctx 的 trace_id 非空非全零。
func TestObservabilityUnified_GRPCUnary(t *testing.T) {
	rec := &obsSpanRecorder{}
	var handlerTID string
	un := UnaryServerInterceptor(Observability(ObservabilityWithTracer(rec)))
	_, err := un(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Get"},
		func(ctx context.Context, _ any) (any, error) {
			handlerTID = ctxTraceID(ctx)
			return nil, nil
		})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if len(rec.kinds) != 1 || rec.kinds[0] != trace.SpanKindServer {
		t.Fatalf("unary span kinds = %v, want one SpanKindServer", rec.kinds)
	}
	if handlerTID == "" || handlerTID == "00000000000000000000000000000000" {
		t.Errorf("unary handler trace_id 应非空非全零, got %q", handlerTID)
	}
}

// TestObservabilityUnified_GRPCStream：经 StreamServerInterceptor 桥，无 incoming
// metadata 的流 handler 仍从 stream.Context() 读到 trace_id——钉住桥用链传播 ctx
// 重包流的修复（链前捕获 wrapped 会整类丢失注入）。
func TestObservabilityUnified_GRPCStream(t *testing.T) {
	rec := &obsSpanRecorder{}
	var streamTID string
	st := StreamServerInterceptor(Observability(ObservabilityWithTracer(rec), ObservabilityWithStream()))
	err := st(nil, &fakeServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: "/svc/Watch"},
		func(_ any, ss grpc.ServerStream) error {
			streamTID = ctxTraceID(ss.Context())
			return nil
		})
	if err != nil {
		t.Fatalf("stream handler error: %v", err)
	}
	if len(rec.kinds) != 1 || rec.kinds[0] != trace.SpanKindServer {
		t.Fatalf("stream span kinds = %v, want one SpanKindServer", rec.kinds)
	}
	if streamTID == "" || streamTID == "00000000000000000000000000000000" {
		t.Errorf("stream handler trace_id 应非空非全零（无 metadata 流也必须包装）, got %q", streamTID)
	}
}

// TestObservabilityUnified_Skip：跳过判定命中则不起 span、直接透传 next。
func TestObservabilityUnified_Skip(t *testing.T) {
	rec := &obsSpanRecorder{}
	called := false
	un := UnaryServerInterceptor(Observability(
		ObservabilityWithTracer(rec),
		ObservabilityWithSkip(func(Transporter) bool { return true }),
	))
	_, err := un(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Get"},
		func(ctx context.Context, _ any) (any, error) {
			called = true
			return nil, nil
		})
	if err != nil || !called {
		t.Fatalf("skip 应透传 next: called=%v err=%v", called, err)
	}
	if len(rec.kinds) != 0 {
		t.Errorf("skip 命中不应起 span, got %v", rec.kinds)
	}
}

// TestObservabilityUnified_NoTransporter：无 Transporter（未经桥装配）退化为
// 透传 next，不起 span。
func TestObservabilityUnified_NoTransporter(t *testing.T) {
	rec := &obsSpanRecorder{}
	called := false
	next := func(ctx context.Context, _ any) (any, error) {
		called = true
		return "ok", nil
	}
	resp, err := Observability(ObservabilityWithTracer(rec))(next)(context.Background(), nil)
	if err != nil || !called || resp != "ok" {
		t.Fatalf("无 Transporter 应透传: resp=%v called=%v err=%v", resp, called, err)
	}
	if len(rec.kinds) != 0 {
		t.Errorf("无 Transporter 不应起 span, got %v", rec.kinds)
	}
}