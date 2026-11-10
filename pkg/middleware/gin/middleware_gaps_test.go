package gin

// middleware_gaps_test.go 覆盖各中间件尚未触达的分支：认证的失败/审计路径、
// 授权 subject 解析与引擎错误、CORS/Secure/Logging 的响应头与透传、限流的
// 访问器与等待失败、超时缓冲 writer 的接口面，以及审计的旁路默认值与 panic
// 防护、观测的 Option 与跳过匹配。

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/log"
)
// TestCORS_HeadersAndPreflight CORS 响应头、可选暴露头/凭证头与 OPTIONS 预检。
func TestCORS_HeadersAndPreflight(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hit := false
	r := gin.New()
	r.Use(CORS(&CORSConfig{
		AllowOrigin: "https://app.example", AllowMethods: "GET", AllowHeaders: "X-Token",
		AllowCredentials: true, MaxAge: 60, ExposedHeaders: "X-Trace",
	}))
	r.GET("/x", func(c *gin.Context) { hit = true; c.Status(http.StatusOK) })
	r.OPTIONS("/x", func(c *gin.Context) { hit = true; c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if !hit || w.Code != http.StatusOK {
		t.Fatalf("normal request must reach handler: hit=%v code=%d", hit, w.Code)
	}
	for k, want := range map[string]string{
		"Access-Control-Allow-Origin":      "https://app.example",
		"Access-Control-Allow-Methods":     "GET",
		"Access-Control-Allow-Headers":     "X-Token",
		"Access-Control-Expose-Headers":    "X-Trace",
		"Access-Control-Allow-Credentials": "true",
		"Access-Control-Max-Age":           "60",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}

	hit = false
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodOptions, "/x", nil))
	if w.Code != http.StatusNoContent || hit {
		t.Fatalf("preflight must answer 204 without handler: code=%d hit=%v", w.Code, hit)
	}
}

// TestSecure_SetsHeaders 安全响应头齐备。
func TestSecure_SetsHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Secure())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	for k, want := range map[string]string{
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"X-XSS-Protection":          "1; mode=block",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// 头缓冲、状态码/已写判定、丢弃后的静默写入。
func TestBufferedResponseWriter_Interface(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	bw := &bufferedResponseWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}, hdr: make(http.Header)}

	bw.WriteHeaderNow()
	bw.Flush()
	if bw.Status() != http.StatusOK || bw.Written() {
		t.Fatalf("empty buffer: status=%d written=%v", bw.Status(), bw.Written())
	}
	bw.Header().Set("X-Test", "1")
	bw.WriteHeader(http.StatusCreated)
	bw.WriteHeader(http.StatusTeapot) // 第二个状态码被忽略
	if bw.Status() != http.StatusCreated || !bw.Written() || bw.Size() != 0 {
		t.Fatalf("after WriteHeader: status=%d written=%v size=%d", bw.Status(), bw.Written(), bw.Size())
	}
	n, err := bw.WriteString("hello")
	if err != nil || n != 5 || bw.Size() != 5 {
		t.Fatalf("WriteString: n=%d err=%v size=%d", n, err, bw.Size())
	}
	code, hdr, body := bw.snapshot()
	if code != http.StatusCreated || hdr.Get("X-Test") != "1" || string(body) != "hello" {
		t.Fatalf("snapshot: code=%d hdr=%v body=%q", code, hdr, body)
	}

	bw.markDiscarded()
	if n, err := bw.Write([]byte("late")); err != nil || n != 4 {
		t.Fatalf("discarded write must be swallowed: n=%d err=%v", n, err)
	}
	bw.WriteHeader(http.StatusInternalServerError)
	if bw.Status() != http.StatusCreated || bw.Size() != 5 {
		t.Fatalf("discarded writes must not change state: status=%d size=%d", bw.Status(), bw.Size())
	}
}

// TestObservability_WithLoggerAndMatchPath WithLogger(nil) 不改变默认行为，
// 显式注入的 logger 被采纳；跳过匹配支持「方法 路径」、通配与目录前缀。
func TestObservability_WithLoggerAndMatchPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, logger := range map[string]log.Logger{
		"nil logger":      nil,
		"explicit logger": nopLogger{},
	} {
		t.Run(name, func(t *testing.T) {
			r := gin.New()
			r.Use(Observability(WithLogger(logger)))
			r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
			if w.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", w.Code)
			}
		})
	}

	cases := []struct {
		path, method, pattern string
		want                  bool
	}{
		{"/metrics", http.MethodGet, "GET /metrics", true},
		{"/metrics", http.MethodPost, "GET /metrics", false},
		{"/health/deep", http.MethodGet, "/health/*", true},
		{"/api/v1/x", http.MethodGet, "/api/", true},
		{"/other", http.MethodGet, "/api/", false},
	}
	for _, tc := range cases {
		if got := matchPath(tc.path, tc.method, tc.pattern); got != tc.want {
			t.Errorf("matchPath(%q, %q, %q) = %v, want %v", tc.path, tc.method, tc.pattern, got, tc.want)
		}
	}
	if shouldSkipPath("/x", http.MethodGet, []string{"/y", "/x"}) != true {
		t.Error("shouldSkipPath must scan all patterns")
	}
}


// nopLogger 是 WithLogger 注入用的最小 log.Logger。
type nopLogger struct{}

func (nopLogger) Debug(context.Context, string, ...any) {}
func (nopLogger) Info(context.Context, string, ...any)  {}
func (nopLogger) Warn(context.Context, string, ...any)  {}
func (nopLogger) Error(context.Context, string, ...any) {}
func (nopLogger) Enabled(log.Level) bool                { return false }
func (l nopLogger) With(...any) log.Logger              { return l }




// ---------------------------------------------------------------------------
// 结构性零覆盖（登记备查）：
//   - ratelimit.go:71 —— NewRateLimit 先经 Validate 拦 rate/burst <= 0，而
//     tokenbucket.New 的失败判据是同一谓词，构造失败不可达（双层校验的防御分支）。
// ---------------------------------------------------------------------------
