package gin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/contextx"
)

// TestObservability_NoOpTraceIDs：未装配全局 TracerProvider（no-op tracer）时，
// 中间件挂到 ctx 属性流的 trace_id/span_id 必须是随机 ID 而非全零，且每请求唯一
// ——否则日志无法按请求关联（回归：曾恒为 32/16 个 0）。
func TestObservability_NoOpTraceIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Observability())

	attr := func(c *gin.Context, key string) string {
		for _, a := range log.ContextAttrs(c.Request.Context()) {
			if a.Key == key {
				return a.Value.String()
			}
		}
		return ""
	}

	var firstTraceID string
	r.GET("/v1/ping", func(c *gin.Context) {
		firstTraceID = attr(c, "trace_id")
		if sid := attr(c, "span_id"); sid == "" || sid == "0000000000000000" {
			t.Errorf("span_id should be random under no-op tracer, got %q", sid)
		}
		c.Status(http.StatusOK)
	})
	r.GET("/v1/pong", func(c *gin.Context) {
		if tid := attr(c, "trace_id"); tid == firstTraceID {
			t.Errorf("trace_id must differ per request: %q", tid)
		}
		c.Status(http.StatusOK)
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/ping", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/pong", nil))

	if firstTraceID == "" || firstTraceID == "00000000000000000000000000000000" {
		t.Fatalf("trace_id should be random under no-op tracer, got %q", firstTraceID)
	}
}

// spanKindRecorder 拦截 Start 的 SpanKind（纯 otel API 实现，不引入 sdk——
// 核心模块零 exporter 依赖的纪律不破）。嵌入 trace.Tracer 接口携带未导出
// 标记方法 tracer()（中间件只调 Start，嵌入方法永不触达）。
type spanKindRecorder struct {
	trace.Tracer
	kinds []trace.SpanKind
}

func (r *spanKindRecorder) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	cfg := trace.NewSpanStartConfig(opts...)
	r.kinds = append(r.kinds, cfg.SpanKind())
	return ctx, trace.SpanFromContext(ctx)
}

// TestObservability_SpanKindServer：inbound HTTP span 必须 SpanKindServer
// （OTel 语义；spanmetrics 服务端聚合与服务拓扑图依赖该值——回归：曾缺省
// internal 被漏计）。同包测试直接替换包级 tracer，零全局 provider 污染。
func TestObservability_SpanKindServer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := &spanKindRecorder{}
	prev := tracer
	tracer = rec
	defer func() { tracer = prev }()

	r := gin.New()
	r.Use(Observability())
	r.POST("/v1/login", func(c *gin.Context) { c.Status(http.StatusUnauthorized) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/login", nil))

	if len(rec.kinds) != 1 {
		t.Fatalf("expected exactly 1 span, got %d", len(rec.kinds))
	}
	if rec.kinds[0] != trace.SpanKindServer {
		t.Errorf("inbound HTTP span kind = %v, want SpanKindServer", rec.kinds[0])
	}
}

// TestObservability_TraceIDInContext：Observability 必须把 trace_id 注入
// contextx（审计-链路关联修复的防回归锚点）——handler 内
// contextx.TraceIDFromContext 必须非空且与日志属性流的 trace_id 同值。
// R4 复审连带发现：此前 trace_id 只进日志属性流，audit/authn/crudbridge
// 五处 TraceIDFromContext 消费恒读空，审计事件 trace_id 字段恒空。
func TestObservability_TraceIDInContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Observability())

	var ctxTID, attrTID string
	r.GET("/v1/ping", func(c *gin.Context) {
		ctxTID = contextx.TraceIDFromContext(c.Request.Context())
		for _, a := range log.ContextAttrs(c.Request.Context()) {
			if a.Key == "trace_id" {
				attrTID = a.Value.String()
			}
		}
		c.Status(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/ping", nil))

	if ctxTID == "" || ctxTID == "00000000000000000000000000000000" {
		t.Errorf("handler 内 TraceIDFromContext 应非空非全零（no-op 兜底随机 ID）, got %q", ctxTID)
	}
	if ctxTID != attrTID {
		t.Errorf("contextx 注入值 %q 应与日志属性 trace_id %q 同源同值（审计关联一致性）", ctxTID, attrTID)
	}
}
