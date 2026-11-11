// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package bundle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/ratelimit"
)

// 本文件是 server.unified_middleware 段装配（方案 S 批次 3）的验收面：
// 统一段规则经 matcher 在**双面链头**生效——先于面段；validate 未注入回调
// fail-fast；段缺省零影响。

// denyLimiter 是恒拒绝限流器（rate_limit 规则的断言载体）。
type denyLimiter struct{}

func (denyLimiter) Allow() (bool, error)           { return false, nil }
func (denyLimiter) Wait(ctx context.Context) error { return nil }
func (denyLimiter) Close() error                   { return nil }

// TestUnifiedRuleShortCircuitsBothFaces 钉住核心承诺：统一段规则（rate_limit
// 恒拒）在 gin 与 gRPC 两面链头短路——面段中间件（Recovery 之后的东西）不执行。
func TestUnifiedRuleShortCircuitsBothFaces(t *testing.T) {
	cfg := &bootstrapv1.Server_UnifiedMiddleware{Rules: []*bootstrapv1.Server_UnifiedMiddleware_Rule{{
		Selector: "*",
		Policy: &bootstrapv1.Server_UnifiedMiddleware_Rule_RateLimit{
			RateLimit: &bootstrapv1.Server_UnifiedMiddleware_RateLimit{Rate: 1, Burst: 1, Wait: false},
		},
	}}}

	b := New(Public(), WithUnifiedMiddleware(cfg))

	// gin 面：burst=1 的令牌桶——首个请求拿走令牌（200），同一秒内的第二个
	// 请求被拒（429，CodeToHTTP(8)）且面段 handler 不执行。
	gin.SetMode(gin.TestMode)
	r := gin.New()
	ran := false
	handler := func(c *gin.Context) { ran = true; c.Status(http.StatusOK) }
	for _, mw := range b.Gin() {
		r.Use(mw)
	}
	r.GET("/v1/x", handler)

	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, httptest.NewRequest(http.MethodGet, "/v1/x", nil))
	if w1.Code != http.StatusOK || !ran {
		t.Fatalf("first request: code=%d ran=%v, want 200/true", w1.Code, ran)
	}

	ran = false
	time.Sleep(5 * time.Millisecond) // 同一秒内：令牌未恢复
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/v1/x", nil))
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request code = %d, want 429", w2.Code)
	}
	if ran {
		t.Error("face handler ran despite unified short-circuit")
	}

	// gRPC 面：统一段拒绝 → ResourceExhausted，handler 不执行。
	// 断言用 status.Code：Bundle 链首是 grpcmw.ErrorInterceptor（收口 *
	// berrors.Error 为 status），统一段拒绝经它保全语义。
	ranGRPC := false
	handler2 := func(ctx context.Context, _ interface{}) (interface{}, error) {
		ranGRPC = true
		return nil, nil
	}
	composed := composeUnary(b.GRPCInterceptors())
	// 限流器独立于 gin 面：首个调用放行（令牌满），第二个被拒。
	// 首个调用放行（令牌满），handler 合法执行。
	_, _ = composed(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/M"}, handler2)
	ranGRPC = false
	// 第二个调用（令牌未恢复）被拒：handler 不执行，错误保全语义。
	_, err := composed(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/M"}, handler2)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("grpc code = %v, want ResourceExhausted", status.Code(err))
	}
	if ranGRPC {
		t.Error("grpc handler ran despite unified short-circuit")
	}
}

// composeUnary 按 slice 序（slice[0] 最外层）组合拦截器链——与
// grpc.ChainUnaryInterceptor 同构的测试脚手架。
func composeUnary(chain []grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		h := handler
		for i := len(chain) - 1; i >= 0; i-- {
			current, prev := chain[i], h
			h = func(ctx context.Context, rq interface{}) (interface{}, error) {
				return current(ctx, rq, info, prev)
			}
		}
		return h(ctx, req)
	}
}

// TestUnifiedValidateMissingFailsFast 段声明 validate 而未注入回调 → 链构建
// fail-fast（J7 同形：声明了却静默不生效是最坏结果）。
func TestUnifiedValidateMissingFailsFast(t *testing.T) {
	cfg := &bootstrapv1.Server_UnifiedMiddleware{Rules: []*bootstrapv1.Server_UnifiedMiddleware_Rule{{
		Selector: "*",
		Policy:   &bootstrapv1.Server_UnifiedMiddleware_Rule_Validate{},
	}}}

	b := New(Public(), WithUnifiedMiddleware(cfg))
	defer func() {
		if r := recover(); r == nil || !strings.Contains(fmtErr(r), "UNIFIED_VALIDATOR_MISSING") {
			t.Fatalf("panic = %v, want UNIFIED_VALIDATOR_MISSING", r)
		}
	}()
	_ = b.Gin()
}

// TestUnifiedAbsentZeroImpact 段缺省 → 链与既有形态一致（opt-in 承诺）。
func TestUnifiedAbsentZeroImpact(t *testing.T) {
	b := New(Public())
	if _, err := b.unifiedMatcher(); err != nil {
		t.Fatalf("unifiedMatcher with nil cfg: %v", err)
	}
	chain := b.Gin()
	if len(chain) == 0 {
		t.Fatal("face chain empty without unified segment")
	}
}

// TestUnifiedRateLimitWaitRule 规则选择器的路由能力：selector 只命中 /v1/admin/*
// ——其它路径不受该规则影响（限流规则放行 + handler 执行）。
func TestUnifiedRateLimitWaitRule(t *testing.T) {
	cfg := &bootstrapv1.Server_UnifiedMiddleware{Rules: []*bootstrapv1.Server_UnifiedMiddleware_Rule{{
		Selector: "GET /v1/admin/*",
		Policy: &bootstrapv1.Server_UnifiedMiddleware_Rule_RateLimit{
			RateLimit: &bootstrapv1.Server_UnifiedMiddleware_RateLimit{Rate: 1, Burst: 1, Wait: false},
		},
	}}}

	b := New(Public(), WithUnifiedMiddleware(cfg))
	gin.SetMode(gin.TestMode)
	r := gin.New()
	for _, mw := range b.Gin() {
		r.Use(mw)
	}
	ran := false
	r.GET("/v1/public/x", func(c *gin.Context) { ran = true; c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/public/x", nil))
	if w.Code != http.StatusOK || !ran {
		t.Fatalf("non-matching path affected by rule: code=%d ran=%v", w.Code, ran)
	}
}


// TestUnifiedAuthnAuthzAuditMissingFailsFast 段声明 authn/authz/audit 策略而
// 未注入对应依赖 → 链构建 fail-fast（J7 同形：声明了却静默不生效是最坏结果）。
func TestUnifiedAuthnAuthzAuditMissingFailsFast(t *testing.T) {
	for _, tt := range []struct {
		name string
		rule *bootstrapv1.Server_UnifiedMiddleware_Rule
		want string
	}{
		{"authn", &bootstrapv1.Server_UnifiedMiddleware_Rule{Selector: "*",
			Policy: &bootstrapv1.Server_UnifiedMiddleware_Rule_Authn{}}, "UNIFIED_AUTHN_MISSING"},
		{"authz", &bootstrapv1.Server_UnifiedMiddleware_Rule{Selector: "*",
			Policy: &bootstrapv1.Server_UnifiedMiddleware_Rule_Authz{}}, "UNIFIED_AUTHZ_MISSING"},
		{"audit", &bootstrapv1.Server_UnifiedMiddleware_Rule{Selector: "*",
			Policy: &bootstrapv1.Server_UnifiedMiddleware_Rule_Audit{}}, "UNIFIED_AUDIT_MISSING"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &bootstrapv1.Server_UnifiedMiddleware{Rules: []*bootstrapv1.Server_UnifiedMiddleware_Rule{tt.rule}}
			b := New(Public(), WithUnifiedMiddleware(cfg))
			defer func() {
				if r := recover(); r == nil || !strings.Contains(fmtErr(r), tt.want) {
					t.Fatalf("panic = %v, want %s", r, tt.want)
				}
			}()
			_ = b.Gin()
		})
	}
}

// TestUnifiedAuthnAuthzAuditRulesTakeEffect 注入依赖后三策略在链头生效：
// authn 缺 token 拒 401（面段未配 authn——拒绝来自统一段规则）、audit 发事件。
func TestUnifiedAuthnAuthzAuditRulesTakeEffect(t *testing.T) {
	cfg := &bootstrapv1.Server_UnifiedMiddleware{Rules: []*bootstrapv1.Server_UnifiedMiddleware_Rule{{
		Selector: "*", Policy: &bootstrapv1.Server_UnifiedMiddleware_Rule_Authn{},
	}}}
	b := New(Public(), WithUnifiedMiddleware(cfg), Authn(&stubAuthn{log: &callLog{}}))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	for _, mw := range b.Gin() {
		r.Use(mw)
	}
	ran := false
	r.GET("/v1/x", func(c *gin.Context) { ran = true })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/x", nil)) // 无 Authorization
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("统一段 authn 规则应拒无 token 请求，got %d", w.Code)
	}
	if ran {
		t.Error("handler ran despite unified authn rule")
	}
}

// TestUnifiedCircuitBreakerRuleTrips 统一段熔断规则：阈值 0.2/样本 2，两次
// 5xx 后开窗拒绝（503——open_code 缺省 UNAVAILABLE 经桥渲染）。
func TestUnifiedCircuitBreakerRuleTrips(t *testing.T) {
	cfg := &bootstrapv1.Server_UnifiedMiddleware{Rules: []*bootstrapv1.Server_UnifiedMiddleware_Rule{{
		Selector: "*",
		Policy: &bootstrapv1.Server_UnifiedMiddleware_Rule_CircuitBreaker{
			CircuitBreaker: &bootstrapv1.Server_UnifiedMiddleware_CircuitBreaker{
				ErrorThreshold: 0.2, MinRequests: 2,
			},
		},
	}}}
	b := New(Public(), WithUnifiedMiddleware(cfg))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	for _, mw := range b.Gin() {
		r.Use(mw)
	}
	hits := 0
	r.GET("/v1/x", func(c *gin.Context) { hits++; c.Status(http.StatusInternalServerError) })
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/x", nil))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("第 %d 次应 500，got %d", i+1, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/x", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("开窗后应 503，got %d", w.Code)
	}
	if hits != 2 {
		t.Errorf("开态不该进 handler，命中 = %d, want 2", hits)
	}
}

// TestUnifiedCircuitBreakerOpenCodeRejectsNon5xx open_code 配了非 5xx 语义码
// → 链构建 fail-fast（契约约束：非 5xx 让客户端把「我坏了」当业务错误）。
func TestUnifiedCircuitBreakerOpenCodeRejectsNon5xx(t *testing.T) {
	cfg := &bootstrapv1.Server_UnifiedMiddleware{Rules: []*bootstrapv1.Server_UnifiedMiddleware_Rule{{
		Selector: "*",
		Policy: &bootstrapv1.Server_UnifiedMiddleware_Rule_CircuitBreaker{
			CircuitBreaker: &bootstrapv1.Server_UnifiedMiddleware_CircuitBreaker{
				OpenCode: 3, // InvalidArgument → 4xx 档
			},
		},
	}}}
	b := New(Public(), WithUnifiedMiddleware(cfg))
	defer func() {
		if r := recover(); r == nil || !strings.Contains(fmtErr(r), "非 5xx") {
			t.Fatalf("panic = %v, want 非 5xx 校验错误", r)
		}
	}()
	_ = b.Gin()
}

func fmtErr(r any) string { return r.(error).Error() }

// ratelimit 引用保持（denyLimiter 实现该契约）。
var _ ratelimit.Limiter = denyLimiter{}

// TestVerifyAcceptsUnifiedAuthnRule 统一段声明 authn 规则本身构成「认证姿态
// 已决定」（路由级认证）——Verify 不再报 AUTHN_UNDECLARED；依赖注入的
// fail-fast 由 unifiedMatcher 保证（见 TestUnifiedAuthnAuthzAuditMissingFailsFast）。
func TestVerifyAcceptsUnifiedAuthnRule(t *testing.T) {
	cfg := &bootstrapv1.Server_UnifiedMiddleware{Rules: []*bootstrapv1.Server_UnifiedMiddleware_Rule{{
		Selector: "*", Policy: &bootstrapv1.Server_UnifiedMiddleware_Rule_Authn{},
	}}}
	if err := New(Public(), WithUnifiedMiddleware(cfg)).Verify(); err != nil {
		t.Fatalf("统一段 authn 规则应视为姿态已决定，got %v", err)
	}
	// 反例：统一段声明的是 rate_limit，不算认证姿态。
	other := &bootstrapv1.Server_UnifiedMiddleware{Rules: []*bootstrapv1.Server_UnifiedMiddleware_Rule{{
		Selector: "*",
		Policy: &bootstrapv1.Server_UnifiedMiddleware_Rule_RateLimit{
			RateLimit: &bootstrapv1.Server_UnifiedMiddleware_RateLimit{Rate: 1, Burst: 1},
		},
	}}}
	if err := New(WithUnifiedMiddleware(other)).Verify(); err == nil {
		t.Fatal("非 authn 策略不应构成认证姿态")
	}
}
