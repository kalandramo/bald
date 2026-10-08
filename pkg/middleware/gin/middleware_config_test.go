package gin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gf "github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/pkg/contextx"
)

// ---------------------------------------------------------------------------
// 契约中间件段的参数触达（server.http.middleware.*）
//
// 背景：该契约段下七个子段此前**零消费者**（能力在跑但配置面未接线，或能力
// 根本不存在）。本文件验证各段配置**确实生效于 HTTP 可观察行为**。
// ---------------------------------------------------------------------------

// --- recovery.stack_trace ---

func TestRecovery_StackTraceConfig(t *testing.T) {
	run := func(cfg ...RecoveryConfig) (int, string) {
		r := gf.New()
		r.Use(Recovery(cfg...))
		r.GET("/boom", func(*gf.Context) { panic("kaboom") })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))
		return w.Code, w.Body.String()
	}

	code, body := run()
	if code != 500 {
		t.Fatalf("缺省 Recovery() 应 500，got %d", code)
	}
	if strings.Contains(body, "stack") {
		t.Errorf("缺省不应含 stack（现有行为不变），got %s", body)
	}

	code, body = run(RecoveryConfig{StackTrace: true})
	if code != 500 {
		t.Fatalf("StackTrace=true 仍应 500，got %d", code)
	}
	if !strings.Contains(body, "stack") {
		t.Errorf("recovery.stack_trace=true 应让响应体含 stack，got %s", body)
	}
}

// --- request_id.header_name ---

func TestRequestID_CustomHeaderName(t *testing.T) {
	run := func(header string, incoming map[string]string) *httptest.ResponseRecorder {
		r := gf.New()
		r.Use(RequestIDMiddleware(RequestIDConfig{HeaderName: header}))
		r.GET("/x", func(c *gf.Context) { c.Status(200) })
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		for k, v := range incoming {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 自定义头名：读入该头并回写同名头。
	w := run("X-Trace-Id", map[string]string{"X-Trace-Id": "abc-123"})
	if got := w.Header().Get("X-Trace-Id"); got != "abc-123" {
		t.Errorf("request_id.header_name=X-Trace-Id 应读取并回写该头，got %q", got)
	}
	if w.Header().Get("X-Request-ID") != "" {
		t.Error("自定义头名时不应再写默认头 X-Request-ID")
	}

	// 缺省头名仍是 X-Request-ID。
	w = run("", nil)
	if w.Header().Get("X-Request-ID") == "" {
		t.Error("缺省应使用 X-Request-ID")
	}
}

// --- logging.skip_paths ---

func TestLogging_SkipPathsAppendsToBuiltin(t *testing.T) {
	// 直接验证选项装配：自定义跳过路径应进入 SkipPaths，且内置表仍在。
	o := &ObservabilityOptions{}
	WithSkipMetrics()(o)
	WithSkipPaths("/custom/skip")(o)

	var hasCustom, hasBuiltin bool
	for _, p := range o.SkipPaths {
		if p == "/custom/skip" {
			hasCustom = true
		}
		if p == "/healthz" {
			hasBuiltin = true
		}
	}
	if !hasCustom {
		t.Error("logging.skip_paths 应进入 SkipPaths")
	}
	if !hasBuiltin {
		t.Error("追加语义：内置跳过表不应被替换掉")
	}

	// 行为层：跳过路径不发日志。
	// 用 shouldSkipPath 直接判定（日志输出的捕获依赖全局 logger，脆弱）。
	if !shouldSkipPath("/custom/skip", http.MethodGet, o.SkipPaths) {
		t.Error("自定义跳过路径应被 shouldSkipPath 命中")
	}
	if !shouldSkipPath("/healthz", http.MethodGet, o.SkipPaths) {
		t.Error("内置跳过路径应仍命中")
	}
}

// --- timeout ---

func TestTimeout_ConfigurableStatusAndDuration(t *testing.T) {
	run := func(cfg TimeoutConfig, sleep time.Duration) (int, string) {
		r := gf.New()
		r.Use(Timeout(cfg))
		r.GET("/slow", func(c *gf.Context) {
			time.Sleep(sleep)
			c.String(200, "done")
		})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/slow", nil))
		return w.Code, w.Body.String()
	}

	// 超时 → 默认 503。
	code, _ := run(TimeoutConfig{DefaultTimeoutMs: 30}, 300*time.Millisecond)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("超时未指定 status_code 应 503，got %d", code)
	}

	// 超时 → 自定义 status_code。
	code, _ = run(TimeoutConfig{DefaultTimeoutMs: 30, StatusCode: 504}, 300*time.Millisecond)
	if code != 504 {
		t.Fatalf("timeout.status_code=504 应生效，got %d", code)
	}

	// 未超时 → 正常响应透传（缓冲 writer 不破坏正常路径）。
	code, body := run(TimeoutConfig{DefaultTimeoutMs: 5000}, 0)
	if code != 200 || body != "done" {
		t.Fatalf("未超时应正常返回 200/done，got %d/%q", code, body)
	}
}

// TestTimeout_HandlerWriteAfterTimeoutDiscarded 超时后 handler 的写入被丢弃，
// 不 panic、不污染已发出的超时响应。
func TestTimeout_HandlerWriteAfterTimeoutDiscarded(t *testing.T) {
	r := gf.New()
	r.Use(Timeout(TimeoutConfig{DefaultTimeoutMs: 30}))
	r.GET("/slow", func(c *gf.Context) {
		time.Sleep(200 * time.Millisecond)
		c.String(200, "late-write-should-be-dropped")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/slow", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("应返回超时码，got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "late-write") {
		t.Errorf("超时后的写入应被丢弃，got body=%q", w.Body.String())
	}
}

// --- tracing（span-only）---

func TestTracing_NoLogButTraceIDInContext(t *testing.T) {
	var gotTraceID string
	r := gf.New()
	r.Use(Tracing())
	r.GET("/t", func(c *gf.Context) {
		gotTraceID = contextx.TraceIDFromContext(c.Request.Context())
		c.Status(200)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/t", nil))

	if w.Code != 200 {
		t.Fatalf("tracing 层不应影响响应，got %d", w.Code)
	}
	if gotTraceID == "" {
		t.Error("tracing 应把 trace_id 注入请求 ctx（审计/日志关联的落点）")
	}
}

// --- cors 规范性约束（`*` + credentials 是浏览器禁止的组合）---

func TestCORSConfig_ValidateRejectsWildcardWithCredentials(t *testing.T) {
	// 非法组合：通配来源 + 凭证。
	bad := &CORSConfig{AllowOrigin: "*", AllowCredentials: true}
	if err := bad.Validate(); err == nil {
		t.Fatal("allowed_origins=[\"*\"] + allow_credentials=true 应被拒（浏览器会拒绝该响应）")
	}

	// 合法：显式来源 + 凭证。
	ok1 := &CORSConfig{AllowOrigin: "https://a.example", AllowCredentials: true}
	if err := ok1.Validate(); err != nil {
		t.Errorf("显式来源 + 凭证应合法: %v", err)
	}

	// 合法：通配来源 + 无凭证（默认配置即此）。
	ok2 := DefaultCORS()
	if err := ok2.Validate(); err != nil {
		t.Errorf("DefaultCORS 应合法: %v", err)
	}
}
