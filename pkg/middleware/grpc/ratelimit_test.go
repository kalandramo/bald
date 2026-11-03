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

// newTestBucket 建一个「补率极低」的真桶：初始令牌 = burst，之后几乎不再补，
// 于是断言可以只关心「扣了几次」而不与定时器赛跑。
func newTestBucket(t *testing.T, burst int) ratelimit.Limiter {
	t.Helper()
	l, err := tokenbucket.New(0.0001, float64(burst))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// TestRateLimitStreamInterceptor_AllowPassesSameStream 放行时把**原 stream 对象**
// 交给 handler：这一层不需要替换上下文，包装会让流的收发多一层委托。
func TestRateLimitStreamInterceptor_AllowPassesSameStream(t *testing.T) {
	ss := &fakeStream{ctx: context.Background()}
	var seen grpc.ServerStream
	err := callStream(RateLimitStreamInterceptor(&stubLimiter{allowOK: true}, false), ss,
		func(s grpc.ServerStream) error { seen = s; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if seen != grpc.ServerStream(ss) {
		t.Fatal("放行时应原样传流")
	}
}

// TestRateLimitStreamInterceptor_NilLimiterIsPassthrough 未注入 limiter 不拦路
// （bundle 侧靠 nil 省略该层，这里兜住直接传 nil 的用法）。
func TestRateLimitStreamInterceptor_NilLimiterIsPassthrough(t *testing.T) {
	var called bool
	err := callStream(RateLimitStreamInterceptor(nil, false), &fakeStream{ctx: context.Background()},
		func(grpc.ServerStream) error { called = true; return nil })
	if err != nil || !called {
		t.Fatalf("err=%v handlerCalled=%v，want (nil, true)", err, called)
	}
}

// TestRateLimitStreamInterceptor_RejectsCarryReason 三个拒绝点（没令牌 / limiter
// 报错 / 排队失败）都必须是 *berrors.Error + ResourceExhausted + 稳定 Reason，
// 且 handler 未被触达。错误码到客户端的转换由 ErrorStreamInterceptor 负责
// （见 error_test.go），这里断的是本层交出去的东西本身。
func TestRateLimitStreamInterceptor_RejectsCarryReason(t *testing.T) {
	cases := []struct {
		name string
		lim  ratelimit.Limiter
		wait bool
	}{
		{"no_token", &stubLimiter{allowOK: false}, false},
		{"limiter_error", &stubLimiter{allowOK: true, allowErr: ratelimit.ErrLimited}, false},
		{"wait_failed", &stubLimiter{waitErr: context.Canceled}, true},
	}
	for _, tc := range cases {
		var called bool
		err := callStream(RateLimitStreamInterceptor(tc.lim, tc.wait), &fakeStream{ctx: context.Background()},
			func(grpc.ServerStream) error { called = true; return nil })
		if called {
			t.Errorf("%s: 被限流的流不应触达 handler", tc.name)
		}
		berr, ok := berrors.FromError(err)
		if !ok || berr.Reason != ReasonRateLimited {
			t.Fatalf("%s: 应得到 Reason=%s 的 *berrors.Error, got %+v (err=%v)", tc.name, ReasonRateLimited, berr, err)
		}
		if berr.Code != berrors.CodeResourceExhausted {
			t.Errorf("%s: Code = %d, want %d", tc.name, berr.Code, berrors.CodeResourceExhausted)
		}
	}
}

// TestRateLimitStreamInterceptor_OneTokenPerStream 计费口径钉死：一条流只在
// **建立时**扣一个令牌，流内消息不再消耗。少了这条，「按流」与「按消息」两种
// 设计就无法在测试里区分，长流会被误按消息计费（或反过来，实现被改成按消息
// 也没人拦）。
func TestRateLimitStreamInterceptor_OneTokenPerStream(t *testing.T) {
	l := newTestBucket(t, 2)

	stream := func() error {
		return callStream(RateLimitStreamInterceptor(l, false), &fakeStream{ctx: context.Background()},
			func(s grpc.ServerStream) error {
				for i := 0; i < 5; i++ { // 流内狂发也不扣令牌
					if err := s.SendMsg("chunk"); err != nil {
						return err
					}
				}
				return nil
			})
	}
	if err := stream(); err != nil {
		t.Fatalf("第 1 条流应放行: %v", err)
	}
	if err := stream(); err != nil {
		t.Fatalf("第 2 条流应放行: %v", err)
	}
	if err := stream(); err == nil {
		t.Fatal("burst=2 用完后第 3 条流应被拒")
	}

	ss := &fakeStream{ctx: context.Background()}
	if got := ss.sends; got != 0 {
		t.Errorf("被拒的流不该有消息发出，sends=%d", got)
	}
}

// TestRateLimitStreamInterceptor_ShareBucketWithUnary stream 与 unary 共用同一个
// 桶：这是 bundle 把一对 Option 驱动两条链的全部意义。若两条链各建一个桶，
// 「HTTP 与 gRPC 各扣一份配额」的漏风会重新出现。
func TestRateLimitStreamInterceptor_ShareBucketWithUnary(t *testing.T) {
	l := newTestBucket(t, 1)

	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	if _, err := RateLimitInterceptor(l, false)(context.Background(), nil, info,
		func(context.Context, any) (any, error) { return "ok", nil }); err != nil {
		t.Fatalf("unary 首个调用应放行: %v", err)
	}

	err := callStream(RateLimitStreamInterceptor(l, false), &fakeStream{ctx: context.Background()},
		func(grpc.ServerStream) error {
			t.Error("配额已被 unary 用掉，流不该建立")
			return nil
		})
	if err == nil {
		t.Fatal("期望被限流")
	}
	if berr, ok := berrors.FromError(err); !ok || berr.Reason != ReasonRateLimited {
		t.Fatalf("Reason 应为 %s，got %+v", ReasonRateLimited, berr)
	}
}

// TestRateLimitStreamInterceptor_WaitUsesStreamCtx wait 模式排队必须拿到**流的
// ctx**：客户端断开后要立刻退出排队，否则慢建流会堆 goroutine。
// 用 stub 断的是「传的是哪个 ctx」，真桶断的是「取消时真的拒绝」。
func TestRateLimitStreamInterceptor_WaitUsesStreamCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := &stubLimiter{} // Wait 预设成功：这里只验 ctx 的来路
	err := callStream(RateLimitStreamInterceptor(s, true), &fakeStream{ctx: ctx},
		func(grpc.ServerStream) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if s.waitSeen != 1 || s.allowSeen != 0 {
		t.Fatalf("wait=true 应只走 Wait：waitSeen=%d allowSeen=%d", s.waitSeen, s.allowSeen)
	}
	if s.waitCtx == nil || s.waitCtx.Err() == nil {
		t.Fatal("Wait 收到的不该是已取消的流 ctx——传错 ctx 等于断开后继续排队")
	}

	// 真桶 + 已空 + 已取消：排队必须立刻以限流拒绝收口，而不是阻塞到令牌补齐。
	l := newTestBucket(t, 1)
	if err := callStream(RateLimitStreamInterceptor(l, true), &fakeStream{ctx: context.Background()},
		func(grpc.ServerStream) error { return nil }); err != nil {
		t.Fatalf("首个流应放行: %v", err)
	}
	var called bool
	err = callStream(RateLimitStreamInterceptor(l, true), &fakeStream{ctx: ctx},
		func(grpc.ServerStream) error { called = true; return nil })
	if called {
		t.Error("ctx 已取消时不应执行 handler")
	}
	if berr, ok := berrors.FromError(err); !ok || berr.Reason != ReasonRateLimited {
		t.Fatalf("取消的排队应以 Reason=%s 拒绝，got %+v (err=%v)", ReasonRateLimited, berr, err)
	}
}
