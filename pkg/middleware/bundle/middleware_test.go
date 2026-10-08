package bundle

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gf "github.com/gin-gonic/gin"
	"google.golang.org/protobuf/encoding/protojson"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// ---------------------------------------------------------------------------
// server.http.middleware.* 契约桥端到端验收
//
// 从**配置文本**（protojson，字段名与 YAML 契约一致）出发，经 FromMiddleware
// 桥 → bundle → 真实 gin 引擎 + httptest，断言 HTTP 可观察行为。
//
// 这直接对应用户验收判据：配置某中间件段 → 该段实际生效。
// ---------------------------------------------------------------------------

func mustMiddleware(t *testing.T, jsonCfg string) *bootstrapv1.Server_Http_Middleware {
	t.Helper()
	mw := &bootstrapv1.Server_Http_Middleware{}
	if err := protojson.Unmarshal([]byte(jsonCfg), mw); err != nil {
		t.Fatalf("解析配置失败: %v", err)
	}
	return mw
}

// TestFromMiddleware_NilIsNoOp 无 middleware 段 → 无 Option、close 安全可调。
func TestFromMiddleware_NilIsNoOp(t *testing.T) {
	opts, closeFn, err := FromMiddleware(nil)
	if err != nil {
		t.Fatalf("nil 不应报错: %v", err)
	}
	if len(opts) != 0 {
		t.Fatalf("nil 应返回零 Option，got %d", len(opts))
	}
	if closeFn == nil {
		t.Fatal("close 应总非 nil（无资源时为 no-op）")
	}
	if err := closeFn(); err != nil {
		t.Fatalf("no-op close 不应报错: %v", err)
	}
}

// TestFromMiddleware_CORSEnforced cors 段 → 响应头带出契约值 + 预检 204。
func TestFromMiddleware_CORSEnforced(t *testing.T) {
	mw := mustMiddleware(t, `{
	  "cors": {
	    "allowedOrigins": ["https://a.example", "https://b.example"],
	    "allowedMethods": ["GET","POST"],
	    "allowedHeaders": ["X-Custom"],
	    "exposedHeaders": ["X-Total-Count"],
	    "allowCredentials": true,
	    "maxAge": 3600
	  }
	}`)
	opts, closeFn, err := FromMiddleware(mw)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	r := gf.New()
	r.Use(New(opts...).Gin()...)
	r.GET("/x", func(c *gf.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://a.example, https://b.example" {
		t.Errorf("cors.allowed_origins 未生效，got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "GET") {
		t.Errorf("cors.allowed_methods 未生效，got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "X-Custom" {
		t.Errorf("cors.allowed_headers 未生效，got %q", got)
	}
	if got := w.Header().Get("Access-Control-Expose-Headers"); got != "X-Total-Count" {
		t.Errorf("cors.exposed_headers 未生效，got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("cors.allow_credentials 未生效，got %q", got)
	}
	if got := w.Header().Get("Access-Control-Max-Age"); got != "3600" {
		t.Errorf("cors.max_age 未生效，got %q", got)
	}

	// 预检。
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodOptions, "/x", nil))
	if w.Code != http.StatusNoContent {
		t.Errorf("OPTIONS 预检应 204，got %d", w.Code)
	}
}

// TestFromMiddleware_RecoveryStackTrace recovery 段 → panic 响应含/不含 stack。
func TestFromMiddleware_RecoveryStackTrace(t *testing.T) {
	mk := func(cfg string) string {
		opts, closeFn, err := FromMiddleware(mustMiddleware(t, cfg))
		if err != nil {
			t.Fatal(err)
		}
		defer closeFn()
		r := gf.New()
		r.Use(New(opts...).Gin()...)
		r.GET("/boom", func(*gf.Context) { panic("x") })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))
		if w.Code != 500 {
			t.Fatalf("应 500，got %d", w.Code)
		}
		return w.Body.String()
	}
	if strings.Contains(mk(`{"recovery":{"stackTrace":false}}`), "stack") {
		t.Error("stack_trace=false 不应含 stack")
	}
	if !strings.Contains(mk(`{"recovery":{"stackTrace":true}}`), "stack") {
		t.Error("recovery.stack_trace=true 应含 stack")
	}
}

// TestFromMiddleware_RequestIDHeader request_id 段 → 自定义头名生效。
func TestFromMiddleware_RequestIDHeader(t *testing.T) {
	opts, closeFn, err := FromMiddleware(mustMiddleware(t, `{"requestId":{"headerName":"X-Corr-Id"}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	r := gf.New()
	r.Use(New(opts...).Gin()...)
	r.GET("/x", func(c *gf.Context) { c.Status(200) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	if w.Header().Get("X-Corr-Id") == "" {
		t.Error("request_id.header_name=X-Corr-Id 未生效")
	}
}

// TestFromMiddleware_TimeoutEnforcedAndFailsFast timeout 段 → 超时生效；非法值 fail-fast。
func TestFromMiddleware_TimeoutEnforcedAndFailsFast(t *testing.T) {
	// 合法：超时返回契约 status_code。
	opts, closeFn, err := FromMiddleware(mustMiddleware(t, `{"timeout":{"defaultTimeoutMs":30,"statusCode":504}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	r := gf.New()
	r.Use(New(opts...).Gin()...)
	r.GET("/slow", func(c *gf.Context) { time.Sleep(250 * time.Millisecond); c.Status(200) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/slow", nil))
	if w.Code != 504 {
		t.Errorf("timeout 段未生效（应 504），got %d", w.Code)
	}

	// 非法：显式声明 timeout 段却给 0 → fail-fast。
	if _, _, err := FromMiddleware(mustMiddleware(t, `{"timeout":{"defaultTimeoutMs":0}}`)); err == nil {
		t.Error("timeout.default_timeout_ms<=0 应 fail-fast")
	}
}

// TestFromMiddleware_RateLimitEnforced rate_limit 段 → 超限 429（与 D5 一致，
// 此处验证经统一桥仍生效）。
func TestFromMiddleware_RateLimitEnforced(t *testing.T) {
	opts, closeFn, err := FromMiddleware(mustMiddleware(t, `{"rateLimit":{"rate":0.0001,"burst":2,"wait":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	r := gf.New()
	r.Use(New(opts...).Gin()...)
	r.GET("/x", func(c *gf.Context) { c.Status(200) })

	codes := []int{}
	for i := 0; i < 4; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		codes = append(codes, w.Code)
	}
	if codes[0] != 200 || codes[1] != 200 {
		t.Errorf("突发容量内应放行，got %v", codes)
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Errorf("超限应 429，got %v", codes)
	}
}

// TestFromMiddleware_AllSevenSegments 七个段一次性声明 → 全部装配、无人报错
// （回归保护：任一新段引入 panics/编译错都会在此暴露）。
func TestFromMiddleware_AllSevenSegments(t *testing.T) {
	mw := mustMiddleware(t, `{
	  "recovery": {"stackTrace": false},
	  "cors": {"allowedOrigins": ["*"]},
	  "logging": {"skipPaths": ["/healthz","/metrics"]},
	  "requestId": {"headerName": "X-Request-ID"},
	  "tracing": {},
	  "rateLimit": {"rate": 100, "burst": 50},
	  "timeout": {"defaultTimeoutMs": 5000, "statusCode": 503}
	}`)
	opts, closeFn, err := FromMiddleware(mw)
	if err != nil {
		t.Fatalf("全段声明不应报错: %v", err)
	}
	defer closeFn()

	// 期望 7 个 Option（recovery/cors/logging/request_id/tracing/rate_limit/timeout）。
	if len(opts) != 7 {
		t.Fatalf("应产出 7 个 Option，got %d", len(opts))
	}

	// 能构造链并正常服务请求。
	r := gf.New()
	r.Use(New(opts...).Gin()...)
	r.GET("/x", func(c *gf.Context) { c.Status(200) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != 200 {
		t.Fatalf("全段装配后请求应 200，got %d", w.Code)
	}
}

// TestFromMiddleware_CORSValidateEnforced 桥必须**调用** CORS 校验（而非只构造
// 配置）——`allowed_origins:["*"]` 与 `allow_credentials:true` 同时出现时装配报错。
//
// 本测试的由来（如实记录）：对桥做「跳过 cc.Validate()」打靶时，gin/bundle 两包
// 仍全绿——因 gin 侧只直接测了方法本身，未覆盖**桥的调用点**。补此用例封住该缺口。
func TestFromMiddleware_CORSValidateEnforced(t *testing.T) {
	// 非法组合：通配来源 + 凭证 → 装配期必须报错。
	mw := mustMiddleware(t, `{"cors":{"allowedOrigins":["*"],"allowCredentials":true}}`)
	_, _, err := FromMiddleware(mw)
	if err == nil {
		t.Fatal("allowed_origins=[\"*\"] + allow_credentials=true 应使装配 fail-fast（浏览器会拒绝该响应）")
	}
	if !strings.Contains(err.Error(), "cors") {
		t.Errorf("错误信息应指明 cors 段，got: %v", err)
	}

	// 合法组合：显式来源 + 凭证 → 通过。
	mw = mustMiddleware(t, `{"cors":{"allowedOrigins":["https://a.example"],"allowCredentials":true}}`)
	_, closeFn, err := FromMiddleware(mw)
	if err != nil {
		t.Fatalf("显式来源 + 凭证应合法: %v", err)
	}
	defer closeFn()

	// 合法组合：通配来源 + 无凭证 → 通过（默认形态）。
	mw = mustMiddleware(t, `{"cors":{"allowedOrigins":["*"]}}`)
	_, closeFn2, err := FromMiddleware(mw)
	if err != nil {
		t.Fatalf("通配来源 + 无凭证应合法: %v", err)
	}
	defer closeFn2()
}
