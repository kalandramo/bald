package bundle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	gf "github.com/gin-gonic/gin"
	"google.golang.org/protobuf/encoding/protojson"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// ---------------------------------------------------------------------------
// D5 用户级验收
//
// task-anchor 成功判据：「配置 server.http.rate_limit 段 → 限流实际生效
// （并发超限被拒）」。
//
// 本测试刻意从**配置文本**出发（而非直接构造 proto 结构体），走完整链路：
//
//	JSON 配置 ──protojson.Unmarshal──▶ Server_Http_Middleware
//	          ──RateLimitFromMiddleware──▶ *ginmw.RateLimiter
//	          ──bundle.RateLimit──▶ bundle ──.Gin()──▶ 真实 gin 引擎 + httptest
//
// 断言的是 HTTP 层的可观察结果（状态码分布 + handler 命中数守恒），
// 不是内部函数分支。
// ---------------------------------------------------------------------------

// TestAcceptanceD5_ConfigDrivenRateLimitEnforced 并发超限被拒，且放行数恰好等于
// 突发容量（令牌桶守恒——既不放行超发，也不误伤）。
func TestAcceptanceD5_ConfigDrivenRateLimitEnforced(t *testing.T) {
	// 1) 从配置文本解析出契约段（模拟 bootstrap 配置加载）。
	const cfgJSON = `{"rateLimit":{"rate":0.0001,"burst":8,"wait":false}}`
	mw := &bootstrapv1.Server_Http_Middleware{}
	if err := protojson.Unmarshal([]byte(cfgJSON), mw); err != nil {
		t.Fatalf("解析配置失败: %v", err)
	}

	// 交叉核对：protojson 用的是 proto 字段名（rateLimit），确认解析出预期值。
	if got := mw.GetRateLimit(); got == nil || got.GetRate() != 0.0001 || got.GetBurst() != 8 {
		b, _ := json.Marshal(got)
		t.Fatalf("配置解析结果不符: %s", b)
	}

	// 2) 契约段 → limiter（框架桥）。
	rl, err := RateLimitFromMiddleware(mw)
	if err != nil {
		t.Fatalf("RateLimitFromMiddleware: %v", err)
	}
	defer rl.Close()

	// 3) limiter → bundle → gin 链。
	var hits int64
	b := New(RateLimit(rl))
	r := gf.New()
	r.Use(b.Gin()...)
	r.GET("/api/x", func(c *gf.Context) {
		atomic.AddInt64(&hits, 1)
		c.Status(http.StatusOK)
	})

	// 4) 并发打 50 个请求。
	const total, burst = 50, 8
	var ok, limited, other int64
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/x", nil))
			switch w.Code {
			case http.StatusOK:
				atomic.AddInt64(&ok, 1)
			case http.StatusTooManyRequests:
				atomic.AddInt64(&limited, 1)
			default:
				atomic.AddInt64(&other, 1)
			}
		}()
	}
	wg.Wait()

	// 观察窗口内 rate≈0，令牌不补充 → 恰好 burst 个放行。
	if other != 0 {
		t.Fatalf("出现意外状态码 %d 个（应为 200 或 429）", other)
	}
	if ok != burst {
		t.Fatalf("放行数 = %d, want 恰好 %d（令牌桶守恒，不得超发）", ok, burst)
	}
	if limited != total-burst {
		t.Fatalf("被拒数 = %d, want %d", limited, total-burst)
	}
	if hits != burst {
		t.Fatalf("handler 命中数 = %d, want %d（被拒请求不应触达业务）", hits, burst)
	}
	_ = context.Background
}
