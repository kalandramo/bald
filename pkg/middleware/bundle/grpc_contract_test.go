package bundle

// 本文件锁 §6.2 收口后的双传输对称性：RateLimit/Timeout 一对 Option 同时驱动
// gin 与 gRPC 两条链，零配置零开销，链序与 gin 一致（授权之后、超时最内层）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ginmw "github.com/kalandramo/bald/pkg/middleware/gin"
)

// TestGRPCChain_RateLimitEnforced 契约段 → bundle.RateLimit → gRPC 链：突发容量
// 内放行，其后以 RESOURCE_EXHAUSTED 拒绝且不触达 handler（与 gin 侧 429 同语义）。
func TestGRPCChain_RateLimitEnforced(t *testing.T) {
	rl, err := RateLimitFromMiddleware(mwWithRateLimit(0.0001, 2, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	b := New(Public(), RateLimit(rl))
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	var hits int
	ics := b.GRPCInterceptors()
	codesSeen := make([]codes.Code, 0, 4)
	for i := 0; i < 4; i++ {
		_, err := chainInvoke(ics, info,
			func(context.Context, any) (any, error) {
				hits++
				return "ok", nil
			}, authedCtx(), nil)
		codesSeen = append(codesSeen, status.Code(err))
	}

	want := []codes.Code{codes.OK, codes.OK, codes.ResourceExhausted, codes.ResourceExhausted}
	if !equal(codesName(codesSeen), codesName(want)) {
		t.Fatalf("gRPC 限流序列 = %v, want %v", codesSeen, want)
	}
	if hits != 2 {
		t.Fatalf("被限流的调用不应触达 handler，hits=%d want 2", hits)
	}
}

func codesName(cs []codes.Code) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.String()
	}
	return out
}

// TestGRPCChain_RateLimitAfterAuthz 链序：限流在授权**之后**。判据是「配额耗尽的
//
//	limiter 仍能让授权拒绝返回 PERMISSION_DENIED」——若限流挂在授权外侧，这里会变成
//	RESOURCE_EXHAUSTED，403/429 语义混淆。
func TestGRPCChain_RateLimitAfterAuthz(t *testing.T) {
	rl, err := RateLimitFromMiddleware(mwWithRateLimit(0.0001, 1, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	log := &callLog{}
	info := &grpc.UnaryServerInfo{FullMethod: "/admin.v1.SecretService/GetSecret"}

	// 先把唯一的令牌用掉。
	bAllow := New(Authn(&stubAuthn{log: log}), Authz(&stubAuthz{log: log, allow: true}), RateLimit(rl))
	if _, err := chainInvoke(bAllow.GRPCInterceptors(), info,
		func(context.Context, any) (any, error) { return "ok", nil }, authedCtx(), nil); err != nil {
		t.Fatalf("首个调用应放行: %v", err)
	}

	// 配额已空，但授权拒绝必须先到（= 被拒请求不消耗限流令牌）。
	bDeny := New(Authn(&stubAuthn{log: log}), Authz(&stubAuthz{log: log, allow: false}), RateLimit(rl))
	_, err = chainInvoke(bDeny.GRPCInterceptors(), info,
		func(context.Context, any) (any, error) {
			t.Error("授权拒绝后不应执行 handler")
			return nil, nil
		}, authedCtx(), nil)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied（而非被限流改写）(err=%v)", status.Code(err), err)
	}
}

// TestGRPCChain_TimeoutGivesHandlerDeadline 同一个 Timeout Option 也作用于 gRPC：
// handler 拿到带截止期的 ctx；调用方已给的 deadline 不被改写。
func TestGRPCChain_TimeoutGivesHandlerDeadline(t *testing.T) {
	b := New(Public(), Timeout(&ginmw.TimeoutConfig{DefaultTimeoutMs: 50}))
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}

	var seen context.Context
	if _, err := chainInvoke(b.GRPCInterceptors(), info,
		func(c context.Context, _ any) (any, error) { seen = c; return "ok", nil },
		context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	dl, ok := seen.Deadline()
	if !ok {
		t.Fatal("配置了 default_timeout_ms 时 gRPC handler ctx 应带截止期")
	}
	if remaining := time.Until(dl); remaining <= 0 || remaining > time.Second {
		t.Fatalf("剩余 %v，期望接近 50ms", remaining)
	}

	// 调用方自带 deadline：原样放过（不腰斩预算）。
	callerCtx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	var seen2 context.Context
	if _, err := chainInvoke(b.GRPCInterceptors(), info,
		func(c context.Context, _ any) (any, error) { seen2 = c; return "ok", nil },
		callerCtx, nil); err != nil {
		t.Fatal(err)
	}
	if dl2, ok := seen2.Deadline(); !ok || time.Until(dl2) < 59*time.Minute {
		t.Fatalf("调用方的 1h 预算被改写，剩余 %v", time.Until(dl2))
	}
}

// TestGRPCChain_ZeroOverheadWithoutOptions 两个 Option 都不给时链不受影响
// （零开销省略，与既有部署二进制兼容）。
func TestGRPCChain_ZeroOverheadWithoutOptions(t *testing.T) {
	base := len(New(Public()).GRPCInterceptors())
	b := New(Public(), RateLimit(nil), Timeout(nil))
	if got := len(b.GRPCInterceptors()); got != base {
		t.Fatalf("未配置时限流/超时层不应入链：%d vs 基线 %d", got, base)
	}
}

// TestGRPCChain_TimeoutNonPositiveIsOmitted gin 与 gRPC 对 DefaultTimeoutMs<=0
// 的既有语义不同，这里锁住已记录的那个差异：gin 照旧挂层（立即 503），gRPC 省略。
// 要两面对齐得先决定是否给 TimeoutConfig 加 Validate——那是独立的 API 决策。
func TestGRPCChain_TimeoutNonPositiveIsOmitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	b := New(Public(), Timeout(&ginmw.TimeoutConfig{DefaultTimeoutMs: 0}))

	if got := len(b.GRPCInterceptors()); got != 4 {
		t.Fatalf("gRPC 链应省略非正超时层，len=%d want 4", got)
	}

	r := gin.New()
	r.Use(b.Gin()...)
	r.GET("/slow", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/slow", nil))
	if w.Code != ginmw.DefaultTimeoutStatusCode {
		t.Fatalf("gin 侧非正超时按既有语义立即 %d，got %d", ginmw.DefaultTimeoutStatusCode, w.Code)
	}
}

// TestGRPCChain_BothLayersPresent 两层同时配置时都在链上，且 Timeout 在最内层。
// 判据用可观察行为：限流拒绝的请求不进入超时层派生的 ctx（无 deadline），
// 放行的请求才拿到截止期。
func TestGRPCChain_BothLayersPresent(t *testing.T) {
	rl, err := RateLimitFromMiddleware(mwWithRateLimit(0.0001, 1, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	b := New(Public(), RateLimit(rl), Timeout(&ginmw.TimeoutConfig{DefaultTimeoutMs: 40}))
	if got, want := len(b.GRPCInterceptors()), 6; got != want {
		t.Fatalf("len=%d want %d（基线 4 + 限流 + 超时）", got, want)
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	var sawDeadline bool
	if _, err := chainInvoke(b.GRPCInterceptors(), info,
		func(c context.Context, _ any) (any, error) {
			_, sawDeadline = c.Deadline()
			return "ok", nil
		}, authedCtx(), nil); err != nil {
		t.Fatal(err)
	}
	if !sawDeadline {
		t.Error("放行的调用应经过超时层（ctx 带截止期）")
	}

	// 配额已空：限流层（超时的外层）先拒，超时层不该被穿过。
	rejectedDeadline := false
	_, err = chainInvoke(b.GRPCInterceptors(), info,
		func(c context.Context, _ any) (any, error) {
			_, rejectedDeadline = c.Deadline()
			return "ok", nil
		}, authedCtx(), nil)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code = %v, want ResourceExhausted (err=%v)", status.Code(err), err)
	}
	if rejectedDeadline {
		t.Error("被限流的调用不应进入超时层")
	}
}
