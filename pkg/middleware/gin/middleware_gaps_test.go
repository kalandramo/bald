package gin

// middleware_gaps_test.go 覆盖各中间件尚未触达的分支：认证的失败/审计路径、
// 授权 subject 解析与引擎错误、CORS/Secure/Logging 的响应头与透传、限流的
// 访问器与等待失败、超时缓冲 writer 的接口面，以及审计的旁路默认值与 panic
// 防护、观测的 Option 与跳过匹配。

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	"github.com/kalandramo/bald/pkg/metrics"
)

type fakeAuthenticator struct {
	claims *authn.AuthClaims
	err    error
	token  string
}

func (f *fakeAuthenticator) Authenticate(ctx context.Context) (*authn.AuthClaims, error) {
	f.token = authn.TokenFromContext(ctx)
	return f.claims, f.err
}

func (f *fakeAuthenticator) AuthenticateToken(string) (*authn.AuthClaims, error) {
	return f.claims, f.err
}

// TestAuthnMiddleware_Flows 认证中间件的四条路径：nil 认证器透传、缺/畸形
// token、校验失败、成功注入 claims。
func TestAuthnMiddleware_Flows(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newRouter := func(a authn.Authenticator, auditor audit.Auditor, got **authn.AuthClaims) *gin.Engine {
		r := gin.New()
		r.Use(AuthnMiddleware(a, AuthnWithAuditor(auditor)))
		r.GET("/x", func(c *gin.Context) {
			*got = authn.AuthClaimsFromContext(c.Request.Context())
			c.Status(http.StatusOK)
		})
		return r
	}

	t.Run("nil authenticator passes through", func(t *testing.T) {
		var got *authn.AuthClaims
		r := newRouter(nil, nil, &got)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		if w.Code != http.StatusOK || got != nil {
			t.Fatalf("nil authenticator must not authenticate: code=%d claims=%v", w.Code, got)
		}
	})

	for name, header := range map[string]string{
		"missing header": "",
		"wrong scheme":   "Basic abc",
		"empty token":    "Bearer ",
	} {
		t.Run(name, func(t *testing.T) {
			var got *authn.AuthClaims
			mem := &auditMem{}
			r := newRouter(&fakeAuthenticator{}, mem, &got)
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("want 401, got %d (%s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "MISSING_TOKEN") {
				t.Errorf("body should carry MISSING_TOKEN: %s", w.Body.String())
			}
			evs := mem.all()
			if len(evs) != 1 || evs[0].Result != audit.ResultDeny || evs[0].Object != "authn" {
				t.Fatalf("authn failure must be audited, got %+v", evs)
			}
		})
	}

	t.Run("authenticator error", func(t *testing.T) {
		var got *authn.AuthClaims
		mem := &auditMem{}
		r := newRouter(&fakeAuthenticator{err: errors.New("expired")}, mem, &got)
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Bearer t-1")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "UNAUTHENTICATED") {
			t.Fatalf("want 401 UNAUTHENTICATED, got %d %s", w.Code, w.Body.String())
		}
		if evs := mem.all(); len(evs) != 1 || evs[0].Error != "UNAUTHENTICATED" {
			t.Fatalf("failure audit mismatch: %+v", evs)
		}
	})

	t.Run("success publishes claims", func(t *testing.T) {
		var got *authn.AuthClaims
		auth := &fakeAuthenticator{claims: &authn.AuthClaims{Subject: "u-1", TenantID: "t-1"}}
		r := newRouter(auth, nil, &got)
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Bearer t-1")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
		}
		if auth.token != "t-1" {
			t.Errorf("token passed to Authenticate = %q", auth.token)
		}
		if got == nil || got.Subject != "u-1" || got.TenantID != "t-1" {
			t.Fatalf("claims not published: %+v", got)
		}
	})
}

// TestAuthzMiddleware_SubjectResolverAndEngineError 自定义 subject 解析优先于
// claims；授权引擎报错映射为 500 并带 reason。
func TestAuthzMiddleware_SubjectResolverAndEngineError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotSub string
	allow := authz.Func(func(_ context.Context, subject, _, _ string) (bool, error) {
		gotSub = subject
		return true, nil
	})
	r := gin.New()
	r.Use(AuthzMiddleware(allow, WithSubjectResolver(func(*gin.Context) string { return "svc-x" })))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusOK || gotSub != "svc-x" {
		t.Fatalf("subject resolver must win: code=%d subject=%q", w.Code, gotSub)
	}

	boom := authz.Func(func(context.Context, string, string, string) (bool, error) {
		return false, errors.New("policy store down")
	})
	r = gin.New()
	r.Use(AuthzMiddleware(boom))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "AUTHZ_ENGINE_ERROR") {
		t.Fatalf("engine error must map to 500 AUTHZ_ENGINE_ERROR, got %d %s", w.Code, w.Body.String())
	}
}

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

// TestLogging_PassesThrough 三种变参形态都返回可用的透传中间件。
func TestLogging_PassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, mw := range map[string]gin.HandlerFunc{
		"no config":      Logging(),
		"empty config":   Logging(LoggingConfig{}),
		"skip paths set": Logging(LoggingConfig{SkipPaths: []string{"/quiet"}}),
	} {
		t.Run(name, func(t *testing.T) {
			r := gin.New()
			r.Use(mw)
			r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
			if w.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", w.Code)
			}
		})
	}
}

type fakeLimiter struct {
	allow    bool
	allowErr error
	waitErr  error
	closed   bool
}

func (f *fakeLimiter) Allow() (bool, error)       { return f.allow, f.allowErr }
func (f *fakeLimiter) Wait(context.Context) error { return f.waitErr }
func (f *fakeLimiter) Close() error               { f.closed = true; return nil }

// TestRateLimiter_AccessorsAndWaitFailure 访问器暴露底层 limiter 与等待模式；
// 等待失败（ctx 取消/永久耗尽）以 429 结束。
func TestRateLimiter_AccessorsAndWaitFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rl, err := NewRateLimit(RateLimitConfig{Rate: 1, Burst: 1, Wait: true})
	if err != nil {
		t.Fatalf("NewRateLimit: %v", err)
	}
	if rl.Limiter() == nil || !rl.Wait() {
		t.Fatalf("accessors: limiter=%v wait=%v", rl.Limiter(), rl.Wait())
	}
	if err := rl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f := &fakeLimiter{waitErr: errors.New("cancelled")}
	r := gin.New()
	r.Use(RateLimit(f, true))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("wait failure must answer 429, got %d", w.Code)
	}
}

// TestBufferedResponseWriter_Interface 缓冲 writer 的 gin.ResponseWriter 接口面：
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

// TestAuditMiddleware_SubjectResolverAndNilDeps 自定义 subject 解析、缺省
// metrics 记录器与 nil 依赖的旁路空操作。
func TestAuditMiddleware_SubjectResolverAndNilDeps(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mem := &auditMem{}
	r := gin.New()
	r.Use(AuditMiddleware(AuditWithAuditor(mem),
		AuditWithSubjectResolver(func(*gin.Context) string { return "svc-a" })))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	evs := mem.all()
	if len(evs) != 1 || evs[0].Subject != "svc-a" || evs[0].TenantID != "" {
		t.Fatalf("subject resolver event mismatch: %+v", evs)
	}

	// 无 Option：auditor 与 metrics 都退化为 Nop，请求照常。
	r = gin.New()
	r.Use(AuditMiddleware())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("default deps must pass through, got %d", w.Code)
	}

	recordSafely(nil, context.Background(), audit.AuditEvent{})
	emitMetricsSafely(nil, context.Background(), metrics.Event{}, metrics.TransportHTTP, 0)
	emitActiveSafely(nil, context.Background(), metrics.Event{}, metrics.TransportHTTP, 1)
}

type panicRecorder struct{}

func (panicRecorder) Record(context.Context, metrics.Event, metrics.Transport, float64) {
	panic("record boom")
}

func (panicRecorder) RecordActive(context.Context, metrics.Event, metrics.Transport, int64) {
	panic("active boom")
}

// nopLogger 是 WithLogger 注入用的最小 log.Logger。
type nopLogger struct{}

func (nopLogger) Debug(context.Context, string, ...any) {}
func (nopLogger) Info(context.Context, string, ...any)  {}
func (nopLogger) Warn(context.Context, string, ...any)  {}
func (nopLogger) Error(context.Context, string, ...any) {}
func (nopLogger) Enabled(log.Level) bool                { return false }
func (l nopLogger) With(...any) log.Logger              { return l }

// TestAuditMiddleware_ErrorResults 5xx 与 gin 私有错误都降级为 error 结果。
func TestAuditMiddleware_ErrorResults(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("server error status", func(t *testing.T) {
		mem := &auditMem{}
		r := gin.New()
		r.Use(AuditMiddleware(AuditWithAuditor(mem)))
		r.GET("/x", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		if evs := mem.all(); len(evs) != 1 || evs[0].Result != audit.ResultError {
			t.Fatalf("5xx must audit as error: %+v", evs)
		}
	})

	t.Run("gin private error", func(t *testing.T) {
		mem := &auditMem{}
		r := gin.New()
		r.Use(AuditMiddleware(AuditWithAuditor(mem)))
		r.GET("/x", func(c *gin.Context) {
			_ = c.Error(errors.New("handler failed"))
			c.Status(http.StatusOK)
		})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		evs := mem.all()
		if len(evs) != 1 || evs[0].Result != audit.ResultError || !strings.Contains(evs[0].Error, "handler failed") {
			t.Fatalf("gin private error must audit as error: %+v", evs)
		}
	})
}

// TestAuditMiddleware_MetricsPanicSafe 指标记录器 panic 不得打穿 HTTP 链。
func TestAuditMiddleware_MetricsPanicSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AuditMiddleware(AuditWithMetrics(panicRecorder{})))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("panicking metrics recorder must not block handler, got %d", w.Code)
	}
}

// TestRequestInfo_HTTPSAndSplitHostPort scheme 只看 TLS；Host 拆分覆盖无端口/
// 非数字端口/空值。
func TestRequestInfo_HTTPSAndSplitHostPort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	c.Request.TLS = &tls.ConnectionState{}
	c.Request.Host = "api.example:8443"
	info := requestInfo(c)
	if info.Scheme != "https" || info.ServerAddr != "api.example" || info.ServerPort != 8443 {
		t.Fatalf("requestInfo = %+v", info)
	}

	for _, tc := range []struct {
		host, wantAddr string
		wantPort       int
	}{
		{"", "", 0},
		{"api.example", "api.example", 0},
		{"api.example:abc", "api.example", 0},
		{"api.example:8080", "api.example", 8080},
	} {
		addr, port := splitHostPort(tc.host)
		if addr != tc.wantAddr || port != tc.wantPort {
			t.Errorf("splitHostPort(%q) = %q,%d; want %q,%d", tc.host, addr, port, tc.wantAddr, tc.wantPort)
		}
	}
}

// ---------------------------------------------------------------------------
// 结构性零覆盖（登记备查）：
//   - ratelimit.go:71 —— NewRateLimit 先经 Validate 拦 rate/burst <= 0，而
//     tokenbucket.New 的失败判据是同一谓词，构造失败不可达（双层校验的防御分支）。
// ---------------------------------------------------------------------------
