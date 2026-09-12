package gin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"

	"github.com/kalandramo/bald/pkg/contextx"
)

// fixedSpan 携带已知 SpanContext 的只读 span（纯 otel API，零 sdk 依赖——
// 与 observability_test 的 fake tracer 同款纪律）。
type fixedSpan struct {
	trace.Span
	sc trace.SpanContext
}

func (s *fixedSpan) SpanContext() trace.SpanContext { return s.sc }

// TestTraceContext：span 的 TraceID 必须注入 request context，供 handler 内
// contextx.TraceIDFromContext 读取。R4 复审确认本中间件是 contextx.WithTraceID
// 的唯一生产者——audit/authn/crudbridge 五处 TraceIDFromContext 消费依赖它，
// 不挂载则审计事件 trace_id 字段恒空。测试钉住该注入契约防回归。
func TestTraceContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tid, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatalf("构造测试 TraceID: %v", err)
	}
	sid, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatalf("构造测试 SpanID: %v", err)
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})

	r := gin.New()
	r.Use(TraceContext())
	var got string
	r.GET("/v1/ping", func(c *gin.Context) {
		got = contextx.TraceIDFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	// 请求 context 预置携带 span（等价上游中间件已 Start）。
	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	req = req.WithContext(trace.ContextWithSpan(context.Background(), &fixedSpan{sc: sc}))
	r.ServeHTTP(httptest.NewRecorder(), req)

	if got != tid.String() {
		t.Errorf("handler 内 TraceIDFromContext = %q, want %q（审计 trace_id 依赖此注入）", got, tid.String())
	}
}

// TestTraceContext_NoSpan：ctx 无 span（no-op tracer 场景）时注入全零 TraceID
// 而非 panic——与 Observability 的 no-op 兜底（随机 ID）策略不同，本中间件
// 忠实转发 span 上下文，全零值由消费方按空值处理。
func TestTraceContext_NoSpan(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(TraceContext())
	var got string
	r.GET("/v1/ping", func(c *gin.Context) {
		got = contextx.TraceIDFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/ping", nil))

	if got == "" {
		t.Error("无 span 时也应注入全零 TraceID 字符串（SpanContext().TraceID().String() 契约），got 空串")
	}
}
