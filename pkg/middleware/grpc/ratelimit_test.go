package grpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/berrors/grpcerr"
	"github.com/kalandramo/bald/ratelimit"
	"github.com/kalandramo/bald/ratelimit/tokenbucket"
)

// stubLimiter 记录调用并回放预设结果。
type stubLimiter struct {
	allowOK   bool
	allowErr  error
	waitErr   error
	waitCtx   context.Context // Wait 收到的 ctx
	allowSeen int
	waitSeen  int
}

var _ ratelimit.Limiter = (*stubLimiter)(nil)

func (s *stubLimiter) Allow() (bool, error) {
	s.allowSeen++
	return s.allowOK, s.allowErr
}

func (s *stubLimiter) Wait(ctx context.Context) error {
	s.waitSeen++
	s.waitCtx = ctx
	return s.waitErr
}

func (s *stubLimiter) Close() error { return nil }

// runRateLimited 把限流层挂在 Error 层内侧调用一次（与 bundle 链序一致），
// 返回最终 error（已收口为 status）与 handler 是否执行。
func runRateLimited(t *testing.T, l ratelimit.Limiter, wait bool, ctx context.Context) (error, bool) {
	t.Helper()
	var handlerCalled bool
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	limited := RateLimitInterceptor(l, wait)
	_, err := ErrorInterceptor()(ctx, nil, info,
		func(c context.Context, rq any) (any, error) {
			return limited(c, rq, info, func(context.Context, any) (any, error) {
				handlerCalled = true
				return "ok", nil
			})
		})
	return err, handlerCalled
}

// TestRateLimitInterceptor_Allow 令牌充足：放行，handler 执行，无错误。
func TestRateLimitInterceptor_Allow(t *testing.T) {
	err, called := runRateLimited(t, &stubLimiter{allowOK: true}, false, context.Background())
	if err != nil || !called {
		t.Fatalf("err=%v handlerCalled=%v，want (nil, true)", err, called)
	}
}

// TestRateLimitInterceptor_NilLimiterIsPassthrough 未注入 limiter 时不拦路——
// bundle 侧靠 nil 判断省略该层，这里兜住直接传 nil 的用法。
func TestRateLimitInterceptor_NilLimiterIsPassthrough(t *testing.T) {
	var called bool
	_, err := callUnary(RateLimitInterceptor(nil, false), context.Background(),
		func(context.Context) (any, error) { called = true; return "ok", nil })
	if err != nil || !called {
		t.Fatalf("err=%v handlerCalled=%v，want (nil, true)", err, called)
	}
}

// TestRateLimitInterceptor_RejectCarriesCodeAndReason 拒绝必须是
// RESOURCE_EXHAUSTED，且经链首 Error 收口后 Reason 仍可还原——否则跨服务的调用方
// 无法区分「被限流」与「服务坏了」。三个拒绝点（没令牌 / limiter 报错 / 排队失败）
// 逐个覆盖，少断一个就可能有一条路径退化成空 Reason。
func TestRateLimitInterceptor_RejectCarriesCodeAndReason(t *testing.T) {
	cases := []struct {
		name string
		lim  ratelimit.Limiter
		wait bool
	}{
		{"no_token", &stubLimiter{allowOK: false}, false},
		{"limiter_error", &stubLimiter{allowOK: false, allowErr: ratelimit.ErrLimited}, false},
		{"wait_failed", &stubLimiter{waitErr: context.Canceled}, true},
	}
	for _, tc := range cases {
		err, called := runRateLimited(t, tc.lim, tc.wait, context.Background())
		if called {
			t.Fatalf("%s: 被限流的请求不应触达 handler", tc.name)
		}
		if status.Code(err) != codes.ResourceExhausted {
			t.Fatalf("%s: code = %v, want ResourceExhausted (err=%v)", tc.name, status.Code(err), err)
		}
		// 客户端侧还原（grpcerr.FromStatus 是文档里的接收端收口姿势）。
		recovered := grpcerr.FromStatus(status.Convert(err))
		berr, ok := berrors.FromError(recovered)
		if !ok || berr.Reason != ReasonRateLimited {
			t.Fatalf("%s: 应还原出 Reason=%s 的 *berrors.Error, got %+v (err=%v)", tc.name, ReasonRateLimited, berr, err)
		}
		if berr.Code != berrors.CodeResourceExhausted {
			t.Errorf("%s: Code = %d, want %d", tc.name, berr.Code, berrors.CodeResourceExhausted)
		}
		// 跨服务调用方的匹配姿势：按稳定 Reason 匹配，Message 可变不参与。
		if !errors.Is(recovered, berrors.ResourceExhausted(ReasonRateLimited)) {
			t.Errorf("%s: errors.Is 按 Reason 匹配失败", tc.name)
		}
	}
}

// TestRateLimitInterceptor_LimiterErrorRejects limiter 自身报错（而非「没令牌」）
// 同样拒绝——与 gin 侧 `err != nil` 即拒同口径；把错误当成放行等于故障时开门。
func TestRateLimitInterceptor_LimiterErrorRejects(t *testing.T) {
	for _, s := range []*stubLimiter{
		{allowOK: true, allowErr: ratelimit.ErrLimited}, // 矛盾返回值也要拒
		{allowOK: false, allowErr: errors.New("limiter down")},
	} {
		err, called := runRateLimited(t, s, false, context.Background())
		if called {
			t.Errorf("limiter 报错时不应执行 handler")
		}
		if status.Code(err) != codes.ResourceExhausted {
			t.Errorf("code = %v, want ResourceExhausted (err=%v)", status.Code(err), err)
		}
	}
}

// TestRateLimitInterceptor_WaitUsesRequestCtx wait 模式必须把请求 ctx 交给
// limiter：客户端断开后要立刻退出排队，否则慢流量下 goroutine 无限堆积。
func TestRateLimitInterceptor_WaitUsesRequestCtx(t *testing.T) {
	s := &stubLimiter{}
	err, called := runRateLimited(t, s, true, context.Background())
	if err != nil || !called {
		t.Fatalf("err=%v handlerCalled=%v，want (nil, true)", err, called)
	}
	if s.waitSeen != 1 || s.allowSeen != 0 {
		t.Errorf("wait=true 应只走 Wait：waitSeen=%d allowSeen=%d", s.waitSeen, s.allowSeen)
	}
	if s.waitCtx == nil {
		t.Fatal("Wait 未收到 ctx")
	}

	// 真 limiter：桶初值为满，先让带得上的那次放行……
	l, lerr := tokenbucket.New(0.0001, 1) // 补率极低：下一个令牌要等数小时
	if lerr != nil {
		t.Fatal(lerr)
	}
	t.Cleanup(func() { _ = l.Close() })

	e1, called1 := runRateLimited(t, l, true, context.Background())
	if e1 != nil || !called1 {
		t.Fatalf("有令牌时应放行：err=%v handlerCalled=%v", e1, called1)
	}

	// ……桶已空，再用已取消的 ctx 排队：必须立即失败，而不是阻塞到令牌补齐。
	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()

	e2, called2 := runRateLimited(t, l, true, reqCtx)
	if called2 {
		t.Error("ctx 已取消时不应执行 handler")
	}
	if status.Code(e2) != codes.ResourceExhausted {
		t.Fatalf("code = %v, want ResourceExhausted (err=%v)", status.Code(e2), e2)
	}
}

// TestRateLimitInterceptor_WaitErrorRejects Wait 返回错误（取消/关闭/永久耗尽）
// 一律以限流拒绝收口。
func TestRateLimitInterceptor_WaitErrorRejects(t *testing.T) {
	for _, werr := range []error{ratelimit.ErrLimited, context.DeadlineExceeded} {
		err, called := runRateLimited(t, &stubLimiter{waitErr: werr}, true, context.Background())
		if called {
			t.Errorf("Wait=%v 时不应执行 handler", werr)
		}
		if status.Code(err) != codes.ResourceExhausted {
			t.Errorf("Wait=%v：code = %v, want ResourceExhausted", werr, status.Code(err))
		}
	}
}
