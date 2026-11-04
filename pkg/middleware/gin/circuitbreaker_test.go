package gin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gf "github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/circuitbreaker"
)

// ---------------------------------------------------------------------------
// 服务端入口熔断（server.http.middleware.circuit_breaker 的中间件面）
//
// 两类判据分开写：
//   - 用**记录型假熔断器**钉「上报配对」这条契约义务（哪个响应码该记什么、
//     被本层拒掉的不能上报）——这些断言必须与底层算法无关，且能精确指出漏上报；
//   - 用**真 hystrix** 钉可观察形状（打满失败率后拒绝、4xx 不开窗、超时之外、
//     panic 之后探针不锁死）——只有真实现才会暴露半开锁死这类失效点。
//
// 时间参数：窗口一律取 10s（测试期间不旋转桶），sleepWindow 只在需要过半开时
// 用 50ms 并**轮询**而不是定长 sleep（CI 与 -race 下的调度抖动不该判红）。
// ---------------------------------------------------------------------------

// recordingBreaker 记录每一次 Allow/Mark* 调用，并可注入拒绝与放行后的行为。
type recordingBreaker struct {
	mu       sync.Mutex
	allowErr error // 非 nil → Allow 恒拒（模拟开态）
	allowN   int32
	success  int32
	failure  int32
}

func (r *recordingBreaker) Allow() error {
	atomic.AddInt32(&r.allowN, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allowErr
}

func (r *recordingBreaker) MarkSuccess() { atomic.AddInt32(&r.success, 1) }
func (r *recordingBreaker) MarkFailure() { atomic.AddInt32(&r.failure, 1) }
func (r *recordingBreaker) Execute(_ context.Context, fn func() error) error {
	if err := r.Allow(); err != nil {
		return err
	}
	err := fn()
	if err != nil {
		r.MarkFailure()
		return err
	}
	r.MarkSuccess()
	return nil
}
func (r *recordingBreaker) State() circuitbreaker.State { return circuitbreaker.StateClosed }
func (r *recordingBreaker) Close() error                { return nil }

func (r *recordingBreaker) counts() (allow int32, success int32, failure int32) {
	return atomic.LoadInt32(&r.allowN), atomic.LoadInt32(&r.success), atomic.LoadInt32(&r.failure)
}

func (r *recordingBreaker) setReject() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.allowErr = circuitbreaker.ErrCircuitOpen
}

// newBreakerRouter 造一条真实 gin 链：Recovery → 熔断 → handler。
// handler 按 codeFn 的返回码写响应；panicIn 为 true 时 handler 先 panic。
func newBreakerRouter(t *testing.T, cb circuitbreaker.CircuitBreaker, status int, handler func(c *gf.Context)) (*gf.Engine, *int64) {
	t.Helper()
	var hits int64
	r := gf.New()
	r.Use(gf.Recovery())
	r.Use(CircuitBreakerMiddleware(cb, status))
	r.GET("/x", func(c *gf.Context) {
		atomic.AddInt64(&hits, 1)
		handler(c)
	})
	return r, &hits
}

func doRequest(r *gf.Engine) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	return w
}

// --- 配置校验（启动期 fail-fast）---

func TestCircuitBreakerConfig_Validate(t *testing.T) {
	okCases := []struct {
		name string
		cfg  CircuitBreakerConfig
	}{
		{"zero means defaults everywhere", CircuitBreakerConfig{}},
		{"threshold boundary 1", CircuitBreakerConfig{ErrorThreshold: 1}},
		{"500 status", CircuitBreakerConfig{StatusCode: 500}},
		{"599 status", CircuitBreakerConfig{StatusCode: 599}},
	}
	for _, tc := range okCases {
		if err := tc.cfg.Validate(); err != nil {
			t.Errorf("%s: 应合法，got %v", tc.name, err)
		}
	}

	badCases := []struct {
		name string
		cfg  CircuitBreakerConfig
		want string
	}{
		{"threshold over 1", CircuitBreakerConfig{ErrorThreshold: 1.5}, "error_threshold"},
		{"negative threshold", CircuitBreakerConfig{ErrorThreshold: -0.1}, "error_threshold"},
		{"negative min_requests", CircuitBreakerConfig{MinRequests: -1}, "min_requests"},
		{"negative open_duration_ms", CircuitBreakerConfig{OpenDurationMs: -1}, "open_duration_ms"},
		{"negative window_ms", CircuitBreakerConfig{WindowMs: -1}, "window_ms"},
		{"4xx status_code", CircuitBreakerConfig{StatusCode: 404}, "status_code"},
		{"sub-500 status_code", CircuitBreakerConfig{StatusCode: 42}, "status_code"},
	}
	for _, tc := range badCases {
		err := tc.cfg.Validate()
		if err == nil {
			t.Fatalf("%s: 应报错，got nil", tc.name)
		}
		if want := "circuit_breaker." + tc.want; !strings.Contains(err.Error(), want) {
			t.Errorf("%s: 错误文案应含 %q，got %q", tc.name, want, err.Error())
		}
	}
}

// TestNewCircuitBreaker_InvalidConfigFailsFast 显式配了非法值 → 构造即报错，
// 而不是「配了但不生效」。
func TestNewCircuitBreaker_InvalidConfigFailsFast(t *testing.T) {
	if _, err := NewCircuitBreaker(CircuitBreakerConfig{ErrorThreshold: 2}); err == nil {
		t.Fatal("error_threshold=2 应报错")
	}
	c, err := NewCircuitBreaker(CircuitBreakerConfig{})
	if err != nil {
		t.Fatalf("零值应取默认并构造成功: %v", err)
	}
	defer func() { _ = c.Close() }()
	if c.Breaker().State() != circuitbreaker.StateClosed {
		t.Errorf("新熔断器应 Closed，got %v", c.Breaker().State())
	}
}

// --- 上报配对义务（假熔断器，与算法无关）---

func TestCircuitBreakerMiddleware_ReportingPairing(t *testing.T) {
	tests := []struct {
		name        string
		handler     func(c *gf.Context)
		wantSuccess int32
		wantFailure int32
		wantHits    int64
		wantStatus  int
	}{
		{"2xx 记成功", func(c *gf.Context) { c.Status(200) }, 1, 0, 1, 200},
		{"3xx 记成功", func(c *gf.Context) { c.Status(302) }, 1, 0, 1, 302},
		{"4xx 记成功（调用方的错不算服务侧失败）",
			func(c *gf.Context) { c.Status(404) }, 1, 0, 1, 404},
		{"500 记失败", func(c *gf.Context) { c.Status(500) }, 0, 1, 1, 500},
		{"503 记失败（内层超时写出的码要能被看见）",
			func(c *gf.Context) { c.Status(503) }, 0, 1, 1, 503},
		{"handler panic 记失败并继续外抛", func(c *gf.Context) { panic("boom") }, 0, 1, 1, 500},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rb := &recordingBreaker{}
			r, hits := newBreakerRouter(t, rb, 0, tc.handler)
			w := doRequest(r)

			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			if got := atomic.LoadInt64(hits); got != tc.wantHits {
				t.Errorf("handler 命中 = %d, want %d", got, tc.wantHits)
			}
			allow, success, failure := rb.counts()
			if allow != 1 {
				t.Errorf("Allow = %d, want 1", allow)
			}
			if success != tc.wantSuccess || failure != tc.wantFailure {
				t.Errorf("Mark* = (success %d, failure %d), want (%d, %d)",
					success, failure, tc.wantSuccess, tc.wantFailure)
			}
		})
	}
}

// TestCircuitBreakerMiddleware_RejectedRequestReportsNothing 被本层拒掉的请求
// 不欠上报：Allow 已返回错误，再 Mark* 等于让拒绝喂养自己的失败率。
func TestCircuitBreakerMiddleware_RejectedRequestReportsNothing(t *testing.T) {
	rb := &recordingBreaker{}
	rb.setReject()
	r, hits := newBreakerRouter(t, rb, http.StatusServiceUnavailable, func(c *gf.Context) { c.Status(200) })

	w := doRequest(r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("开态应回 %d，got %d", http.StatusServiceUnavailable, w.Code)
	}
	if got := atomic.LoadInt64(hits); got != 0 {
		t.Errorf("被拒请求不应进 handler，got %d 次", got)
	}
	if _, success, failure := rb.counts(); success != 0 || failure != 0 {
		t.Errorf("被拒请求不该上报，got (success %d, failure %d)", success, failure)
	}
}

// TestCircuitBreakerMiddleware_StatusFloorOnZero 契约字段留空（0）时回默认 503
// 而不是 0 —— 一个 0 状态码会被 net/http 当成 200，那是「配了却更坏了」。
func TestCircuitBreakerMiddleware_StatusFloorOnZero(t *testing.T) {
	rb := &recordingBreaker{}
	rb.setReject()
	r, _ := newBreakerRouter(t, rb, 0, func(c *gf.Context) { c.Status(200) })
	if w := doRequest(r); w.Code != DefaultCircuitBreakerStatusCode {
		t.Errorf("status = %d, want %d", w.Code, DefaultCircuitBreakerStatusCode)
	}
}

// TestCircuitBreakerMiddleware_NilBreakerPassesThrough nil 熔断器 = 这一层透明。
func TestCircuitBreakerMiddleware_NilBreakerPassesThrough(t *testing.T) {
	r, hits := newBreakerRouter(t, nil, 503, func(c *gf.Context) { c.Status(500) })
	if w := doRequest(r); w.Code != 500 {
		t.Errorf("status = %d, want 500（nil 时不该改响应）", w.Code)
	}
	if got := atomic.LoadInt64(hits); got != 1 {
		t.Errorf("handler 命中 = %d, want 1", got)
	}
}

// --- 真 hystrix 的可观察形状 ---

// tripCfg 是测试用的极值：样本量 2、阈值默认 0.5、滑窗 10s（桶不旋转）、
// 开窗 60s（测试期间恒开）。
func tripCfg() CircuitBreakerConfig {
	return CircuitBreakerConfig{MinRequests: 2, OpenDurationMs: 60_000, WindowMs: 10_000}
}

// TestCircuitBreaker_TripsAfterServerErrorVolume 连续两次 5xx 后开窗：第三次
// 直接 503 且不进 handler。
func TestCircuitBreaker_TripsAfterServerErrorVolume(t *testing.T) {
	c, err := NewCircuitBreaker(tripCfg())
	if err != nil {
		t.Fatalf("NewCircuitBreaker: %v", err)
	}
	defer func() { _ = c.Close() }()

	var hits int64
	r := gf.New()
	r.Use(c.Handler())
	r.GET("/x", func(cc *gf.Context) {
		atomic.AddInt64(&hits, 1)
		cc.Status(http.StatusInternalServerError)
	})

	for i := 0; i < 2; i++ {
		if w := doRequest(r); w.Code != http.StatusInternalServerError {
			t.Fatalf("第 %d 次应 500（还没到样本量），got %d", i+1, w.Code)
		}
	}
	if st := c.Breaker().State(); st != circuitbreaker.StateOpen {
		t.Fatalf("两次 5xx 后应开窗，state=%v", st)
	}
	w := doRequest(r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("开态第三次应 503，got %d", w.Code)
	}
	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Errorf("开态不该进 handler，handler 命中 = %d, want 2", got)
	}
}

// TestCircuitBreaker_ClientErrorsNeverTrip 全是 4xx 时永不开窗——一个坏客户端
// 替全体用户开闸是不被接受的形状（这也是失败口径存在的全部理由）。
func TestCircuitBreaker_ClientErrorsNeverTrip(t *testing.T) {
	c, err := NewCircuitBreaker(tripCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	r := gf.New()
	r.Use(c.Handler())
	r.GET("/x", func(cc *gf.Context) { cc.Status(http.StatusBadRequest) })

	for i := 0; i < 20; i++ {
		if w := doRequest(r); w.Code != http.StatusBadRequest {
			t.Fatalf("第 %d 次应仍是 400，got %d —— 4xx 被记成了服务侧失败", i+1, w.Code)
		}
	}
	if st := c.Breaker().State(); st != circuitbreaker.StateClosed {
		t.Errorf("20 次 4xx 后仍应 Closed，state=%v", st)
	}
}

// TestCircuitBreaker_CountsInnerTimeoutStatus 钉链序判据「熔断在超时之外」：
// 内层 Timeout 写出的 504 必须被这一层记成失败。「handler 总是超时」正是
// 熔断该抓的形状——挂在超时内侧就只能看到成功返回的 handler。
func TestCircuitBreaker_CountsInnerTimeoutStatus(t *testing.T) {
	c, err := NewCircuitBreaker(CircuitBreakerConfig{MinRequests: 2, OpenDurationMs: 60_000, WindowMs: 10_000, StatusCode: 503})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	r := gf.New()
	r.Use(c.Handler())
	r.Use(Timeout(TimeoutConfig{DefaultTimeoutMs: 20, StatusCode: 504}))
	r.GET("/x", func(cc *gf.Context) {
		time.Sleep(200 * time.Millisecond)
		cc.Status(http.StatusOK)
	})

	for i := 0; i < 2; i++ {
		if w := doRequest(r); w.Code != http.StatusGatewayTimeout {
			t.Fatalf("第 %d 次应由内层超时回 504，got %d", i+1, w.Code)
		}
	}
	// 第三次由熔断拒绝：不再进 handler，也就不再等那 200ms。
	start := time.Now()
	w := doRequest(r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("第三次应 503（超时写出的 504 没被记成失败？）got %d", w.Code)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("开态请求耗时 %v，want <100ms（说明还在等内层 handler）", elapsed)
	}
}

// TestCircuitBreaker_PanicDoesNotLockHalfOpen 是漏上报的判据：handler panic 时
// 若不在 defer 里上报，半开的在途探针标记永不复位，熔断从此**永久拒绝**。
//
// 形状：只用 panic 把熔断打穿 → 等过半开（轮询到 State 不再是 Open）→ 下一次
// 请求必须真进到 handler（探针放行了）。摘掉上报那行，这里会红在「探针永不放行」。
func TestCircuitBreaker_PanicDoesNotLockHalfOpen(t *testing.T) {
	c, err := NewCircuitBreaker(CircuitBreakerConfig{
		MinRequests:    2,
		OpenDurationMs: 50,
		WindowMs:       10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	var hits int64
	panicHandler := func(cc *gf.Context) {
		atomic.AddInt64(&hits, 1)
		panic("boom")
	}
	r := gf.New()
	r.Use(gf.Recovery())
	r.Use(c.Handler())
	r.GET("/x", panicHandler)

	// 1) 用 panic 把熔断打进 Open。
	for i := 0; i < 4; i++ {
		doRequest(r)
	}
	if st := c.Breaker().State(); st != circuitbreaker.StateOpen {
		t.Fatalf("4 次 panic 后应 Open，state=%v", st)
	}

	// 2) 等休眠窗过（轮询，别用定长 sleep 赌调度）。
	deadline := time.Now().Add(2 * time.Second)
	for c.Breaker().State() == circuitbreaker.StateOpen {
		if time.Now().After(deadline) {
			t.Fatal("等半开超时（sleepWindow=50ms 早该到期）")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// 3) 探针必须真被放行——这一条就是「上报没漏」的证据。
	before := atomic.LoadInt64(&hits)
	doRequest(r)
	if after := atomic.LoadInt64(&hits); after == before {
		t.Fatal("半开探针没放行：handler 一次都没被调用 —— panic 路径漏上报会让熔断永久拒绝")
	}

	// 4) 探针失败后再次开窗、再过半开，仍应放行第二个探针（不是一次性的）。
	//    handler 恒 panic ⇒ 探针必然重新开窗；读到 Closed 只有一种解释——探针的
	//    失败没被上报（上报被记成了成功），正是这条判据要抓的缺陷，不是「太快恢复」。
	if st := c.Breaker().State(); st != circuitbreaker.StateOpen {
		t.Fatalf("panic 探针之后 state=%v, want Open —— 探针失败没记进熔断器", st)
	}
	deadline = time.Now().Add(2 * time.Second)
	for c.Breaker().State() == circuitbreaker.StateOpen {
		if time.Now().After(deadline) {
			t.Fatal("第二轮等半开超时")
		}
		time.Sleep(2 * time.Millisecond)
	}
	before = atomic.LoadInt64(&hits)
	doRequest(r)
	if after := atomic.LoadInt64(&hits); after == before {
		t.Fatal("第二轮半开探针没放行（永久锁死）")
	}
}

// TestCircuitBreaker_CloseRejectsEverything hystrix 的 Close 之后永久拒绝。
// 记进测试是为了让「closer 只能在停止接流量之后调」这条纪律有可观察后果。
func TestCircuitBreaker_CloseRejectsEverything(t *testing.T) {
	c, err := NewCircuitBreaker(tripCfg())
	if err != nil {
		t.Fatal(err)
	}
	var hits int64
	r := gf.New()
	r.Use(c.Handler())
	r.GET("/x", func(cc *gf.Context) {
		atomic.AddInt64(&hits, 1)
		cc.Status(http.StatusOK)
	})

	if w := doRequest(r); w.Code != http.StatusOK {
		t.Fatalf("关闭前应放行，got %d", w.Code)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close 应幂等，第二次 got %v", err)
	}
	if w := doRequest(r); w.Code != http.StatusServiceUnavailable {
		t.Errorf("Close 后应拒绝，got %d", w.Code)
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Errorf("Close 后不该再进 handler，命中 = %d, want 1", got)
	}
}
