package bundle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	gf "github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/berrors/grpcerr"
	ginmw "github.com/kalandramo/bald/pkg/middleware/gin"
	"github.com/kalandramo/bald/ratelimit"
	"github.com/kalandramo/bald/ratelimit/tokenbucket"
)

// ---------------------------------------------------------------------------
// server.{http,grpc}.middleware.circuit_breaker → 熔断真的挂上链
//
// 全部从**配置文本**出发（protojson，字段名与 YAML 契约一致），走
// FromMiddleware / FromGRPCMiddleware → bundle → 真实 gin 引擎 / 真实 unary 拦截器
// 链。断言的是可观察行为（状态码、gRPC code、handler 是否被调用），不是内部分支。
// ---------------------------------------------------------------------------

// --- 契约字段 → 中间件配置（HTTP 面）---

// TestCircuitBreakerFromMiddleware_FieldsMapped 五个契约字段逐条落到行为上。
// 极值取「样本量 2 + 阈值 0.2 + 开窗 60s」：两次 5xx 必开窗，且测试期间不再放行。
func TestCircuitBreakerFromMiddleware_FieldsMapped(t *testing.T) {
	const cfgJSON = `{"circuitBreaker":{
	  "errorThreshold":0.2,"minRequests":2,
	  "openDurationMs":60000,"windowMs":10000,"statusCode":503}}`
	mw := mustMiddleware(t, cfgJSON)
	cb := mw.GetCircuitBreaker()
	if cb.GetErrorThreshold() != 0.2 || cb.GetMinRequests() != 2 ||
		cb.GetOpenDurationMs() != 60000 || cb.GetWindowMs() != 10000 || cb.GetStatusCode() != 503 {
		t.Fatalf("protojson 没把 circuit_breaker 段解析全：%+v", cb)
	}

	opts, closeFn, err := FromMiddleware(mw)
	if err != nil {
		t.Fatalf("FromMiddleware: %v", err)
	}
	defer func() { _ = closeFn() }()

	var hits int
	r := gf.New()
	r.Use(New(opts...).Gin()...)
	r.GET("/x", func(c *gf.Context) {
		hits++
		c.Status(http.StatusInternalServerError)
	})

	for i := 0; i < 2; i++ {
		if w := serveBreakered(r); w.Code != http.StatusInternalServerError {
			t.Fatalf("第 %d 次应 500，got %d", i+1, w.Code)
		}
	}
	if w := serveBreakered(r); w.Code != http.StatusServiceUnavailable {
		t.Errorf("契约字段没生效：第三次 got %d, want 503（阈值 0.2、样本量 2 时两次 5xx 就该开窗）", w.Code)
	}
	if hits != 2 {
		t.Errorf("开态不该进 handler，命中 = %d, want 2", hits)
	}
}

// TestCircuitBreakerFromMiddleware_StatusFieldIsConsumed status_code 不是摆设：
// 给它 500 时开态回 500 而不是硬编码 503。
func TestCircuitBreakerFromMiddleware_StatusFieldIsConsumed(t *testing.T) {
	opts, closeFn, err := FromMiddleware(mustMiddleware(t,
		`{"circuitBreaker":{"minRequests":1,"openDurationMs":60000,"statusCode":500}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeFn() }()

	r := gf.New()
	r.Use(New(opts...).Gin()...)
	r.GET("/x", func(c *gf.Context) { c.Status(http.StatusInternalServerError) })

	// 一次 500（样本量 1、阈值默认 0.5 → 100% 失败）即开窗。
	if w := serveBreakered(r); w.Code != 500 {
		t.Fatalf("第一发应 500，got %d", w.Code)
	}
	if w := serveBreakered(r); w.Code != 500 {
		t.Errorf("开态应回契约给的 500，got %d —— status_code 字段没被消费", w.Code)
	}
}

// TestCircuitBreakerFromMiddleware_ZeroMeansDefaults 段存在但字段全留空（proto3
// 里 0 与「没写」不可区分）→ 取 hystrix 默认，而不是 fail-fast。
func TestCircuitBreakerFromMiddleware_ZeroMeansDefaults(t *testing.T) {
	opts, closeFn, err := FromMiddleware(mustMiddleware(t, `{"circuitBreaker":{}}`))
	if err != nil {
		t.Fatalf("全零值应合法（取默认），got %v", err)
	}
	defer func() { _ = closeFn() }()

	var hits int
	r := gf.New()
	r.Use(New(opts...).Gin()...)
	r.GET("/x", func(c *gf.Context) { hits++; c.Status(http.StatusInternalServerError) })

	// 默认 min_requests=20，且判定发生在**上报之后**（hystrix.evaluateLocked 在
	// MarkFailure 里）：第 20 发仍进 handler、它那一次上报才把窗打开，被拒从第 21
	// 发起。上面 min_requests=2 那组是同一个形状（两发 500，第三发 503）。
	for i := 0; i < 20; i++ {
		if w := serveBreakered(r); w.Code != http.StatusInternalServerError {
			t.Fatalf("第 %d 次应仍 500（默认样本量 20），got %d", i+1, w.Code)
		}
	}
	if w := serveBreakered(r); w.Code != http.StatusServiceUnavailable {
		t.Errorf("第 21 次应开窗回 503，got %d", w.Code)
	}
	if hits != 20 {
		t.Errorf("handler 命中 = %d, want 20", hits)
	}
}

// TestFromMiddleware_CircuitBreakerInvalidFailsFast 显式配了非法值 → 桥报错，
// 文案带上段名（启动期能看到是哪一段配坏了）。
func TestFromMiddleware_CircuitBreakerInvalidFailsFast(t *testing.T) {
	for _, jsonCfg := range []string{
		`{"circuitBreaker":{"errorThreshold":1.5}}`,
		`{"circuitBreaker":{"errorThreshold":-0.1}}`,
		`{"circuitBreaker":{"minRequests":-1}}`,
		`{"circuitBreaker":{"openDurationMs":-5}}`,
		`{"circuitBreaker":{"windowMs":-1}}`,
		`{"circuitBreaker":{"statusCode":404}}`,
	} {
		opts, closeFn, err := FromMiddleware(mustMiddleware(t, jsonCfg))
		if err == nil {
			t.Fatalf("%s: 应 fail-fast，got %d 个 Option", jsonCfg, len(opts))
		}
		if !strings.Contains(err.Error(), "middleware.circuit_breaker") {
			t.Errorf("%s: 错误文案应含段名，got %q", jsonCfg, err.Error())
		}
		if closeFn == nil {
			t.Error("出错路径的 close 也必须非 nil")
		}
	}
}

// TestFromMiddleware_NoCircuitBreakerSegmentMeansNoLayer 缺段 → 不挂层（opt-in）：
// 连打 10 发 5xx 也全是 500，一次都不拒绝。
func TestFromMiddleware_NoCircuitBreakerSegmentMeansNoLayer(t *testing.T) {
	opts, closeFn, err := FromMiddleware(mustMiddleware(t, `{"logging":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeFn() }()

	var hits int
	r := gf.New()
	r.Use(New(opts...).Gin()...)
	r.GET("/x", func(c *gf.Context) { hits++; c.Status(http.StatusInternalServerError) })
	for i := 0; i < 10; i++ {
		if w := serveBreakered(r); w.Code != http.StatusInternalServerError {
			t.Fatalf("没配熔断段却第 %d 次 got %d —— 熔断被默认打开了", i+1, w.Code)
		}
	}
	if hits != 10 {
		t.Errorf("handler 命中 = %d, want 10", hits)
	}
	if b := New(opts...); ginCBLayers(b.Gin()) != 0 {
		t.Errorf("缺段时链里不该有熔断层，got %d", ginCBLayers(b.Gin()))
	}
}

// TestFromMiddleware_CloseClosesBreaker 桥返回的 close 真的关掉熔断器。
// hystrix 的 Close = 永久开态，所以这条同时钉住「closer 只能在停止接流量后调」。
func TestFromMiddleware_CloseClosesBreaker(t *testing.T) {
	opts, closeFn, err := FromMiddleware(mustMiddleware(t,
		`{"circuitBreaker":{"minRequests":2,"openDurationMs":60000}}`))
	if err != nil {
		t.Fatal(err)
	}

	var hits int
	r := gf.New()
	r.Use(New(opts...).Gin()...)
	r.GET("/x", func(c *gf.Context) { hits++; c.Status(http.StatusOK) })

	if w := serveBreakered(r); w.Code != http.StatusOK {
		t.Fatalf("关闭前应放行，got %d", w.Code)
	}
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if w := serveBreakered(r); w.Code != http.StatusServiceUnavailable {
		t.Errorf("close 后应拒绝，got %d", w.Code)
	}
	if hits != 1 {
		t.Errorf("close 后不该再进 handler，命中 = %d, want 1", hits)
	}
}

// --- gRPC 面 ---

// TestGRPCCircuitBreakerFromMiddleware_FieldsMapped 契约 gRPC 段（无 status_code）
// → unary 链开态回 UNAVAILABLE + Reason=CIRCUIT_OPEN（经链首 Error 收口可还原）。
func TestGRPCCircuitBreakerFromMiddleware_FieldsMapped(t *testing.T) {
	mw := mustGRPCMiddleware(t, `{"circuitBreaker":{
	  "errorThreshold":0.2,"minRequests":2,
	  "openDurationMs":60000,"windowMs":10000}}`)
	opts, closeFn, err := FromGRPCMiddleware(mw)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeFn() }()

	chain := New(append(opts, Public())...).GRPCInterceptors()
	info := &grpc.UnaryServerInfo{FullMethod: "/demo.v1.Greet/Greet"}

	for i := 0; i < 2; i++ {
		_, err := chainInvoke(chain, info, func(context.Context, any) (any, error) {
			return nil, status.Error(codes.Internal, "boom")
		}, context.Background(), nil)
		if got := status.Code(err); got != codes.Internal {
			t.Fatalf("第 %d 次应原样回 Internal，got %v", i+1, got)
		}
	}

	var handlerCalled bool
	_, err = chainInvoke(chain, info, func(context.Context, any) (any, error) {
		handlerCalled = true
		return "ok", nil
	}, context.Background(), nil)
	if handlerCalled {
		t.Error("开态不该进 handler")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("开态第三次 got %v, want UNAVAILABLE", got)
	}
	berr, ok := berrors.FromError(grpcerr.FromStatus(status.Convert(err)))
	if !ok {
		t.Fatalf("拒绝没还原成 *berrors.Error：%T", err)
	}
	if berr.Reason != "CIRCUIT_OPEN" {
		t.Errorf("reason = %q, want CIRCUIT_OPEN", berr.Reason)
	}
}

// TestFromGRPCMiddleware_CircuitBreakerInvalidFailsFast 与 HTTP 侧同纪律：
// 显式配了非法值就报错，文案带 grpc.middleware.circuit_breaker。
func TestFromGRPCMiddleware_CircuitBreakerInvalidFailsFast(t *testing.T) {
	opts, closeFn, err := FromGRPCMiddleware(mustGRPCMiddleware(t,
		`{"circuitBreaker":{"minRequests":-2}}`))
	if err == nil {
		t.Fatalf("min_requests=-2 应 fail-fast，got %d 个 Option", len(opts))
	}
	if !strings.Contains(err.Error(), "grpc.middleware.circuit_breaker") {
		t.Errorf("错误文案应含段名，got %q", err.Error())
	}
	if closeFn == nil {
		t.Error("close 必须非 nil")
	}
}

// TestGRPCCircuitBreaker_TripsOnTimeoutBudget 钉 gRPC 面的两条口径合成：超时预算
// 到期 → handler 交出 DeadlineExceeded → 熔断记失败 → 打满后拒绝。
// 熔断若挂在超时内侧就看不见这次失败，永远不开窗。
func TestGRPCCircuitBreaker_TripsOnTimeoutBudget(t *testing.T) {
	mw := mustGRPCMiddleware(t, `{
	  "timeout":{"defaultTimeoutMs":20},
	  "circuitBreaker":{"minRequests":2,"openDurationMs":60000,"windowMs":10000}}`)
	opts, closeFn, err := FromGRPCMiddleware(mw)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeFn() }()

	chain := New(append(opts, Public())...).GRPCInterceptors()
	info := &grpc.UnaryServerInfo{FullMethod: "/demo.v1.Greet/Greet"}

	var hits int
	run := func() error {
		_, err := chainInvoke(chain, info, func(ctx context.Context, _ any) (any, error) {
			hits++
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
				return "late", nil
			}
		}, context.Background(), nil)
		return err
	}

	for i := 0; i < 2; i++ {
		if got := status.Code(run()); got != codes.DeadlineExceeded {
			t.Fatalf("第 %d 次应由内层超时给出 DeadlineExceeded，got %v", i+1, got)
		}
	}
	if got := status.Code(run()); got != codes.Unavailable {
		t.Errorf("第三次应被熔断拒绝（UNAVAILABLE），got %v", got)
	}
	if hits != 2 {
		t.Errorf("开态不该进 handler，命中 = %d, want 2", hits)
	}
}

// --- 链序与覆盖关系 ---

// TestCircuitBreaker_GinChainPosition 熔断夹在限流与超时之间（外→内）。位置本身
// 就是判据：更外则被限流拒掉的请求去喂养失败率，更内则看不见超时写出的 5xx。
func TestCircuitBreaker_GinChainPosition(t *testing.T) {
	br := mustBreaker(t)
	defer func() { _ = br.Close() }()
	rl := mustRateLimiter(t)
	defer func() { _ = rl.Close() }()

	chain := New(
		Public(), RateLimit(rl, false), CircuitBreaker(br),
		Timeout(&ginmw.TimeoutConfig{DefaultTimeoutMs: 1000}),
	).Gin()

	// 批次 4 换血后限流居统一件段桥内（GinHandler 闭包），按桥名定位段位——
	// 桥段在熔断之前即限流层在熔断之前的既有判据。
	iFace, iCB, iTO := ginIndexByName(chain, "GinHandler"), ginIndex(chain, br.Handler()), ginIndexByName(chain, "Timeout")
	if iFace < 0 || iCB < 0 || iTO < 0 {
		t.Fatalf("三层都应挂上：face=%d breaker=%d timeout=%d", iFace, iCB, iTO)
	}
	if !(iFace < iCB && iCB < iTO) {
		t.Errorf("链序应 统一件段(含限流) → CircuitBreaker → Timeout，got %d / %d / %d", iFace, iCB, iTO)
	}
}

// TestCircuitBreaker_GRPCChainPosition 是它的 gRPC 对偶。
func TestCircuitBreaker_GRPCChainPosition(t *testing.T) {
	br := mustBreaker(t)
	defer func() { _ = br.Close() }()
	rl := mustRateLimiter(t)
	defer func() { _ = rl.Close() }()

	chain := New(
		Public(), RateLimit(rl, false), CircuitBreaker(br), GRPCTimeout(time.Second),
	).GRPCInterceptors()

	// 批次 4 换血后 face 桥（含限流）与 Timeout 桥同为 UnaryServerInterceptor
	// 闭包——前者在前、后者在后，夹住留守的熔断拦截器，既有判据保序。
	iFace, iCB, iTO := grpcIndexByName(chain, "UnaryServerInterceptor"), grpcIndexByName(chain, "CircuitBreakerInterceptor"), grpcLastIndexByName(chain, "UnaryServerInterceptor")
	if iFace < 0 || iCB < 0 || iTO < 0 {
		t.Fatalf("三层都应挂上：face=%d breaker=%d timeout=%d", iFace, iCB, iTO)
	}
	if !(iFace < iCB && iCB < iTO) {
		t.Errorf("链序应 统一件段(含限流) → CircuitBreaker → Timeout，got %d / %d / %d", iFace, iCB, iTO)
	}
}

// TestGRPCCircuitBreaker_NotOnStreamChain stream 面刻意不挂熔断（一条流的失败
// 没有一致答案）。这条断言守的是「别哪天顺手把它挪到 stream 链上」。
func TestGRPCCircuitBreaker_NotOnStreamChain(t *testing.T) {
	br := mustBreaker(t)
	defer func() { _ = br.Close() }()

	if stream := New(CircuitBreaker(br), Public()).GRPCStreamInterceptors(); len(stream) != 0 {
		t.Errorf("只给熔断段时 stream 链应为空，got %d 层", len(stream))
	}
	rl := mustRateLimiter(t)
	defer func() { _ = rl.Close() }()
	stream := New(CircuitBreaker(br), RateLimit(rl, false), Public()).GRPCStreamInterceptors()
	for _, f := range stream {
		if name := funcTag(f); strings.Contains(name, "CircuitBreaker") {
			t.Error("熔断不该出现在 stream 链上")
		}
	}
}

// TestCircuitBreaker_CoverageMatrix gRPC 面的熔断器取 GRPCCircuitBreaker 优先、
// 否则回落 CircuitBreaker，与限流逐条相同；gin 面永远只吃 CircuitBreaker。
func TestCircuitBreaker_CoverageMatrix(t *testing.T) {
	httpCB := mustBreaker(t)
	defer func() { _ = httpCB.Close() }()
	grpcCB := mustBreaker(t)
	defer func() { _ = grpcCB.Close() }()

	// 只给 CircuitBreaker：gRPC 回落共用同一个实例，gin 面挂它。
	shared := New(CircuitBreaker(httpCB), Public())
	if got := shared.grpcBreaker(); got != httpCB {
		t.Error("只给 HTTP 段时 gRPC 应回落共用同一个熔断器")
	}
	if ginIndex(shared.Gin(), httpCB.Handler()) < 0 {
		t.Error("gin 链应挂 HTTP 那个熔断器")
	}

	// 只给 GRPCCircuitBreaker：gRPC 用它，gin 面没有熔断层（各面独立、不互相兜底）。
	grpcOnly := New(GRPCCircuitBreaker(grpcCB), Public())
	if got := grpcOnly.grpcBreaker(); got != grpcCB {
		t.Error("gRPC 应用自己的实例")
	}
	if n := ginCBLayers(grpcOnly.Gin()); n != 0 {
		t.Errorf("只给 GRPCCircuitBreaker 时 gin 面应无熔断层，got %d", n)
	}

	// 两个都给：各用自己的，不存在「后写的 Option 静默盖掉前一个」。
	both := New(CircuitBreaker(httpCB), GRPCCircuitBreaker(grpcCB), Public())
	if got := both.grpcBreaker(); got != grpcCB {
		t.Error("两个都给时 gRPC 应用 GRPCCircuitBreaker 的那个")
	}
	if ginIndex(both.Gin(), httpCB.Handler()) < 0 {
		t.Error("两个都给时 gin 面应仍用 HTTP 那个")
	}
	if n := ginCBLayers(both.Gin()); n != 1 {
		t.Errorf("gin 面应只有一层熔断，got %d", n)
	}
}

// TestFromMiddleware_BothFacesCircuitBreaker 两面各自的段翻成两个独立实例：
// HTTP 面开窗不影响 gRPC 面（两个滑窗互不相干）。
func TestFromMiddleware_BothFacesCircuitBreaker(t *testing.T) {
	httpOpts, httpClose, err := FromMiddleware(mustMiddleware(t,
		`{"circuitBreaker":{"minRequests":1,"openDurationMs":60000,"windowMs":10000}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = httpClose() }()
	grpcOpts, grpcClose, err := FromGRPCMiddleware(mustGRPCMiddleware(t,
		`{"circuitBreaker":{"minRequests":5,"openDurationMs":60000,"windowMs":10000}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = grpcClose() }()

	b := New(append(append([]Option{}, httpOpts...), grpcOpts...)...)

	// HTTP 面：一发 5xx 就开窗（min_requests=1）。
	r := gf.New()
	r.Use(b.Gin()...)
	r.GET("/x", func(c *gf.Context) { c.Status(http.StatusInternalServerError) })
	if w := serveBreakered(r); w.Code != http.StatusInternalServerError {
		t.Fatalf("第一发应 500，got %d", w.Code)
	}
	if w := serveBreakered(r); w.Code != http.StatusServiceUnavailable {
		t.Errorf("HTTP 面第二次应 503（自己的段 min_requests=1），got %d", w.Code)
	}

	// gRPC 面：min_requests=5，两发失败不该开窗。
	chain := b.GRPCInterceptors()
	info := &grpc.UnaryServerInfo{FullMethod: "/demo.v1.Greet/Greet"}
	for i := 0; i < 2; i++ {
		_, err := chainInvoke(chain, info, func(context.Context, any) (any, error) {
			return nil, status.Error(codes.Internal, "boom")
		}, context.Background(), nil)
		if got := status.Code(err); got != codes.Internal {
			t.Fatalf("gRPC 面第 %d 次应仍 Internal（它的桶独立，min_requests=5），got %v", i+1, got)
		}
	}
}

// --- helpers ---

func serveBreakered(r *gf.Engine) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	return w
}

func mustBreaker(t *testing.T) *ginmw.CircuitBreaker {
	t.Helper()
	br, err := ginmw.NewCircuitBreaker(ginmw.CircuitBreakerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return br
}

func mustRateLimiter(t *testing.T) ratelimit.Limiter {
	t.Helper()
	l, err := tokenbucket.New(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// funcTag 取中间件/拦截器的**构造函数名**（闭包形如
// ".../gin.CircuitBreakerMiddleware.func1" → "gin.CircuitBreakerMiddleware.func1"
// → "CircuitBreakerMiddleware"）。链序断言要的是「谁在谁之前」，比数长度精确。
func funcTag(f any) string {
	full := runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
	if i := strings.LastIndex(full, "/"); i >= 0 {
		full = full[i+1:]
	}
	parts := strings.Split(full, ".")
	if len(parts) >= 2 {
		return parts[1]
	}
	return parts[0]
}

// ginIndex 按闭包身份定位链里的层（同一次 Handler() 返回的闭包指针相同）。
func ginIndex(chain []gf.HandlerFunc, target gf.HandlerFunc) int {
	want := reflect.ValueOf(target).Pointer()
	for i, f := range chain {
		if reflect.ValueOf(f).Pointer() == want {
			return i
		}
	}
	return -1
}

func ginIndexByName(chain []gf.HandlerFunc, name string) int {
	for i, f := range chain {
		if funcTag(f) == name {
			return i
		}
	}
	return -1
}

func grpcLastIndexByName(chain []grpc.UnaryServerInterceptor, name string) int {
	last := -1
	for i, f := range chain {
		if funcTag(f) == name {
			last = i
		}
	}
	return last
}

func grpcIndexByName(chain []grpc.UnaryServerInterceptor, name string) int {
	for i, f := range chain {
		if funcTag(f) == name {
			return i
		}
	}
	return -1
}

func ginCBLayers(chain []gf.HandlerFunc) int {
	n := 0
	for _, f := range chain {
		if funcTag(f) == "CircuitBreakerMiddleware" {
			n++
		}
	}
	return n
}
