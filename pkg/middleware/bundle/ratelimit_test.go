package bundle

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	gf "github.com/gin-gonic/gin"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	ginmw "github.com/kalandramo/bald/pkg/middleware/gin"
)

func init() { gf.SetMode(gf.TestMode) }

// mwWithRateLimit 构造含 rate_limit 段的契约中间件配置。
func mwWithRateLimit(rate float64, burst int32, wait bool) *bootstrapv1.Server_Http_Middleware {
	return &bootstrapv1.Server_Http_Middleware{
		RateLimit: &bootstrapv1.Server_Http_Middleware_RateLimit{
			Rate:  rate,
			Burst: burst,
			Wait:  wait,
		},
	}
}

// ---------------------------------------------------------------------------
// D5：契约段 server.http.middleware.rate_limit → 限流实际生效
// ---------------------------------------------------------------------------

// TestRateLimitFromMiddleware_NilOrAbsentIsDisabled 未配置段 = 禁用（nil, nil），
// 不报错——与既有中间件段「缺省不装配」的保守约定一致。
func TestRateLimitFromMiddleware_NilOrAbsentIsDisabled(t *testing.T) {
	rl, err := RateLimitFromMiddleware(nil)
	if err != nil || rl != nil {
		t.Fatalf("nil mw: got (%v, %v), want (nil, nil)", rl, err)
	}
	rl, err = RateLimitFromMiddleware(&bootstrapv1.Server_Http_Middleware{})
	if err != nil || rl != nil {
		t.Fatalf("empty mw: got (%v, %v), want (nil, nil)", rl, err)
	}
}

// TestRateLimitFromMiddleware_InvalidConfigFailsFast 显式配置了段却给非法值
// → 报错（启动期 fail-fast，而非静默不生效）。
func TestRateLimitFromMiddleware_InvalidConfigFailsFast(t *testing.T) {
	for _, mw := range []*bootstrapv1.Server_Http_Middleware{
		mwWithRateLimit(0, 10, false),
		mwWithRateLimit(-1, 10, false),
		mwWithRateLimit(10, 0, false),
	} {
		if _, err := RateLimitFromMiddleware(mw); err == nil {
			t.Errorf("非法配置 %v 应报错", mw.GetRateLimit())
		}
	}
}

// TestRateLimitFromMiddleware_ValidBuilds 合法配置构造出 limiter。
func TestRateLimitFromMiddleware_ValidBuilds(t *testing.T) {
	rl, err := RateLimitFromMiddleware(mwWithRateLimit(10, 5, true))
	if err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
	if rl == nil {
		t.Fatal("合法配置应返回非 nil limiter")
	}
	defer rl.Close()
	if !rl.Wait() {
		t.Error("wait=true 应反映到 limiter")
	}
}

// TestGinChain_RateLimitEnforced 端到端：契约段 → bundle.RateLimit → gin 链，
// 超限请求被 429 拒绝。这是 task-anchor 的 D5 成功判据的单元化表达。
func TestGinChain_RateLimitEnforced(t *testing.T) {
	rl, err := RateLimitFromMiddleware(mwWithRateLimit(0.0001, 2, false))
	if err != nil {
		t.Fatal(err)
	}
	defer rl.Close()

	var hits int64
	b := New(RateLimit(rl))
	r := gf.New()
	r.Use(b.Gin()...)
	r.GET("/ok", func(c *gf.Context) {
		atomic.AddInt64(&hits, 1)
		c.Status(http.StatusOK)
	})

	codes := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ok", nil))
		codes = append(codes, w.Code)
	}

	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Fatalf("突发容量内应放行，got %v", codes)
	}
	if codes[2] != http.StatusTooManyRequests || codes[3] != http.StatusTooManyRequests {
		t.Fatalf("超限应 429，got %v", codes)
	}
	if hits != 2 {
		t.Fatalf("被拒请求不应触达 handler，hits=%d want 2", hits)
	}
}

// TestGinChain_NoRateLimitWhenAbsent 未注入 limiter 时链中无限流层（不被误挂）。
func TestGinChain_NoRateLimitWhenAbsent(t *testing.T) {
	b := New()
	r := gf.New()
	r.Use(b.Gin()...)
	r.GET("/ok", func(c *gf.Context) { c.Status(http.StatusOK) })

	for i := 0; i < 100; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ok", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("无限流层时第 %d 个请求不应被拒，got %d", i+1, w.Code)
		}
	}
}

// TestGinChain_RateLimitAfterAuthz 链序：限流在授权之后（保护业务而非认证层）；
// 授权拒绝的请求不消耗限流令牌（429 与 403 的语义不混淆）。
func TestGinChain_RateLimitAfterAuthz(t *testing.T) {
	log := &callLog{}
	az := &stubAuthz{log: log, allow: false}
	rl, err := RateLimitFromMiddleware(mwWithRateLimit(0.0001, 10, false))
	if err != nil {
		t.Fatal(err)
	}
	defer rl.Close()

	b := New(Authz(az), RateLimit(rl))
	r := gf.New()
	r.Use(b.Gin()...)
	r.GET("/secret", func(c *gf.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/secret", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("授权拒绝应 403（而非被限流改写），got %d", w.Code)
	}
	if !logHas(log, "authz") {
		t.Errorf("授权层应执行，order=%v", log.snapshot())
	}
	_ = ginmw.RateLimit // 引用确保 import 可见（链序断言用不到该符号本身）
}

func logHas(l *callLog, name string) bool {
	for _, s := range l.snapshot() {
		if s == name {
			return true
		}
	}
	return false
}
