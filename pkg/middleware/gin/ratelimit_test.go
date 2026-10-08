package gin

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	gf "github.com/gin-gonic/gin"
)

func init() { gf.SetMode(gf.TestMode) }

// newLimitedRouter 构造一个挂了限流中间件的 gin 引擎，路由 /ok 返回 200。
func newLimitedRouter(t *testing.T, cfg RateLimitConfig, hits *int64) (*gf.Engine, *RateLimiter) {
	t.Helper()
	rl, err := NewRateLimit(cfg)
	if err != nil {
		t.Fatalf("NewRateLimit: %v", err)
	}
	r := gf.New()
	r.Use(rl.Handler())
	r.GET("/ok", func(c *gf.Context) {
		if hits != nil {
			atomic.AddInt64(hits, 1)
		}
		c.Status(http.StatusOK)
	})
	return r, rl
}

// ---------------------------------------------------------------------------
// D5：配置 server.http.rate_limit → 限流实际生效
//
// 用户级验收（task-anchor）：配置 rate_limit 段后，并发/连续超限请求被 429 拒绝
// （而非全部放行）。以下测试直接驱动真实 gin 引擎 + httptest，验证 HTTP 层
// 可观察行为，而非内部函数分支。
// ---------------------------------------------------------------------------

// TestRateLimit_RejectsBeyondBurst 验收核心：burst=2 的桶，前 2 个请求放行
// （消耗初始令牌），第 3 个立即 429 —— 且 handler 未被调用（不进入业务）。
func TestRateLimit_RejectsBeyondBurst(t *testing.T) {
	var hits int64
	r, rl := newLimitedRouter(t, RateLimitConfig{Rate: 0.0001, Burst: 2}, &hits)
	defer rl.Close()

	codes := make([]int, 0, 5)
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ok", nil))
		codes = append(codes, w.Code)
	}

	// 前 2 个应有令牌可用。
	for i := 0; i < 2; i++ {
		if codes[i] != http.StatusOK {
			t.Fatalf("第 %d 个请求应放行(200)，got %d（codes=%v）", i+1, codes[i], codes)
		}
	}
	// 之后必须被拒（rate 极低 → 观察窗口内无补充）。
	for i := 2; i < 5; i++ {
		if codes[i] != http.StatusTooManyRequests {
			t.Fatalf("第 %d 个请求应被限流(429)，got %d（codes=%v）", i+1, codes[i], codes)
		}
	}
	// 被拒的请求不得进入 handler。
	if hits != 2 {
		t.Fatalf("handler 命中数 = %d, want 2（超限请求不应触达业务）", hits)
	}
}

// TestRateLimit_AllowsWithinBurst burst=3 时前 3 个放行（不误伤）。
func TestRateLimit_AllowsWithinBurst(t *testing.T) {
	var hits int64
	r, rl := newLimitedRouter(t, RateLimitConfig{Rate: 1, Burst: 3}, &hits)
	defer rl.Close()

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ok", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("突发容量内的第 %d 个请求应放行，got %d", i+1, w.Code)
		}
	}
	if hits != 3 {
		t.Fatalf("handler 命中数 = %d, want 3", hits)
	}
}

// TestRateLimit_WaitModeDoesNotReject 等待模式下，只要 ctx 未取消，超限请求
// 阻塞等待而非 429（用高 rate 让等待极短）。
func TestRateLimit_WaitModeDoesNotReject(t *testing.T) {
	var hits int64
	r, rl := newLimitedRouter(t, RateLimitConfig{Rate: 1000, Burst: 1, Wait: true}, &hits)
	defer rl.Close()

	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ok", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("等待模式下第 %d 个请求应最终放行，got %d", i+1, w.Code)
		}
	}
	if hits != 5 {
		t.Fatalf("等待模式 handler 命中数 = %d, want 5", hits)
	}
}

// TestNewRateLimit_RejectsInvalidConfig 显式配置了 rate_limit 段却给非正值 =
// 配置错误，fail-fast（而非静默不生效）。
func TestNewRateLimit_RejectsInvalidConfig(t *testing.T) {
	for _, cfg := range []RateLimitConfig{
		{Rate: 0, Burst: 1},
		{Rate: -1, Burst: 1},
		{Rate: 1, Burst: 0},
		{Rate: 1, Burst: -2},
	} {
		if _, err := NewRateLimit(cfg); err == nil {
			t.Errorf("配置 %+v 应被拒绝（fail-fast）", cfg)
		}
	}
}

// TestRateLimiter_CloseIsIdempotent Close 可重复调用（停机路径可能多次触发）。
func TestRateLimiter_CloseIsIdempotent(t *testing.T) {
	rl, err := NewRateLimit(RateLimitConfig{Rate: 1, Burst: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := rl.Close(); err != nil {
		t.Fatalf("首次 Close: %v", err)
	}
	if err := rl.Close(); err != nil {
		t.Fatalf("重复 Close 应幂等: %v", err)
	}
}

// TestRateLimit_ConcurrentBurstExact 并发下恰好 burst 个放行（令牌桶的守恒性
// ——限流不能因并发而超发）。
func TestRateLimit_ConcurrentBurstExact(t *testing.T) {
	const burst = 20
	var hits int64
	r, rl := newLimitedRouter(t, RateLimitConfig{Rate: 0.0001, Burst: burst}, &hits)
	defer rl.Close()

	var wg sync.WaitGroup
	var ok int64
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ok", nil))
			if w.Code == http.StatusOK {
				atomic.AddInt64(&ok, 1)
			}
		}()
	}
	wg.Wait()

	if ok != burst {
		t.Fatalf("并发放行数 = %d, want 恰好 %d（不得超发）", ok, burst)
	}
	if hits != burst {
		t.Fatalf("handler 命中数 = %d, want %d", hits, burst)
	}
}
