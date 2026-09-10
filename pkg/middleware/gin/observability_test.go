package gin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/log"
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
