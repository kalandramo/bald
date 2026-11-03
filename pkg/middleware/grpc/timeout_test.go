package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// callUnary 按 grpc.ChainUnaryInterceptor 的语义调用单个拦截器，
// 避免为一条断言起真实 server。
func callUnary(ic grpc.UnaryServerInterceptor, ctx context.Context,
	handler func(context.Context) (any, error),
) (any, error) {
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	return ic(ctx, nil, info, func(c context.Context, _ any) (any, error) { return handler(c) })
}

// fakeStream 是最小的 grpc.ServerStream：只提供 ctx 与收发计数。收发计数用来
// 证明「拦截器看不见流内消息」这一计费口径（见 TestRateLimitStream_*）。
type fakeStream struct {
	ctx   context.Context
	sends int
}

var _ grpc.ServerStream = (*fakeStream)(nil)

func (s *fakeStream) SetHeader(metadata.MD) error  { return nil }
func (s *fakeStream) SendHeader(metadata.MD) error { return nil }
func (s *fakeStream) SetTrailer(metadata.MD)       {}
func (s *fakeStream) Context() context.Context     { return s.ctx }
func (s *fakeStream) SendMsg(any) error            { s.sends++; return nil }
func (s *fakeStream) RecvMsg(any) error            { return nil }

// callStream 按 grpc.ChainStreamInterceptor 的语义调用单个 stream 拦截器。
func callStream(ic grpc.StreamServerInterceptor, ss grpc.ServerStream,
	handler func(grpc.ServerStream) error,
) error {
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/Stream"}
	return ic(nil, ss, info, func(_ any, s grpc.ServerStream) error { return handler(s) })
}

// TestTimeoutInterceptor_AddsDeadline 调用方没带 deadline 时，handler 拿到的
// ctx 应有一个约等于 d 的截止期——这是本层存在的唯一正面保证。
func TestTimeoutInterceptor_AddsDeadline(t *testing.T) {
	ic := TimeoutInterceptor(100 * time.Millisecond)

	var seen context.Context
	resp, err := callUnary(ic, context.Background(), func(c context.Context) (any, error) {
		seen = c
		return "ok", nil
	})
	if err != nil || resp != "ok" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	dl, ok := seen.Deadline()
	if !ok {
		t.Fatal("调用方未设 deadline 时 handler ctx 应被挂上截止期")
	}
	if remaining := time.Until(dl); remaining <= 0 || remaining > 200*time.Millisecond {
		t.Fatalf("剩余 %v，期望接近 100ms", remaining)
	}
}

// TestTimeoutInterceptor_NeverShortensCallerDeadline 「不腰斩调用方预算」是本层
// 与被腰斩版本的分水岭：已带 deadline 的调用必须原样放过。
func TestTimeoutInterceptor_NeverShortensCallerDeadline(t *testing.T) {
	orig, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	var seen context.Context
	if _, err := callUnary(TimeoutInterceptor(10*time.Millisecond), orig, func(c context.Context) (any, error) {
		seen = c
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	dl, ok := seen.Deadline()
	if !ok {
		t.Fatal("deadline 丢了")
	}
	if time.Until(dl) < 59*time.Minute {
		t.Fatalf("调用方的 1h 预算被改成 %v——本层无权缩短它", time.Until(dl))
	}
}

// TestTimeoutInterceptor_NonPositiveIsPassthrough d<=0（配置段留空最常见的值）
// 等同不装配：不能把 0 当「立即到期」把所有 RPC 打死。
func TestTimeoutInterceptor_NonPositiveIsPassthrough(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		var sawDeadline bool
		if _, err := callUnary(TimeoutInterceptor(d), context.Background(), func(c context.Context) (any, error) {
			_, sawDeadline = c.Deadline()
			return "ok", nil
		}); err != nil {
			t.Fatalf("d=%v err=%v", d, err)
		}
		if sawDeadline {
			t.Errorf("d=%v 不应挂超时", d)
		}
	}
}

// TestTimeoutInterceptor_HandlerSeesExpiry 到点后 handler 的 ctx 必须真的取消——
// 「handler 拿得到一个会到期的上下文」是本层的契约；至于响应是否按时返回，
// 取决于 handler 自己是否监听 ctx.Done（见 TimeoutInterceptor 注释）。
func TestTimeoutInterceptor_HandlerSeesExpiry(t *testing.T) {
	ic := TimeoutInterceptor(10 * time.Millisecond)

	var err error
	resp, callErr := callUnary(ic, context.Background(), func(c context.Context) (any, error) {
		select {
		case <-c.Done():
			err = c.Err()
		case <-time.After(2 * time.Second):
			t.Error("handler ctx 未在 2s 内到期")
		}
		return "done", nil
	})
	if callErr != nil || resp != "done" {
		t.Fatalf("resp=%v err=%v", resp, callErr)
	}
	if err != context.DeadlineExceeded {
		t.Fatalf("ctx.Err() = %v, want DeadlineExceeded", err)
	}
}

// TestTimeoutInterceptor_CancelsOnReturn handler 返回后派生 ctx 必须被 cancel
// （defer cancel()）；否则每次调用泄漏一个 deadline timer。
func TestTimeoutInterceptor_CancelsOnReturn(t *testing.T) {
	var seen context.Context
	if _, err := callUnary(TimeoutInterceptor(time.Minute), context.Background(), func(c context.Context) (any, error) {
		seen = c
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-seen.Done():
	case <-time.After(time.Second):
		t.Fatal("handler 返回后派生 ctx 未被取消（cancel 泄漏）")
	}
}

// TestTimeoutInterceptor_HandlerErrorPassesThrough 本层不改写错误语义：
// handler 的 status 原样上抛，由链首 ErrorInterceptor 负责收口。
func TestTimeoutInterceptor_HandlerErrorPassesThrough(t *testing.T) {
	want := status.Error(codes.NotFound, "gone")
	_, err := callUnary(TimeoutInterceptor(time.Second), context.Background(), func(context.Context) (any, error) {
		return nil, want
	})
	if status.Code(err) != codes.NotFound || err.Error() != want.Error() {
		t.Fatalf("err = %v，want 原样透传 %v", err, want)
	}
}

// TestTimeoutStreamInterceptor_WrapsStreamContext stream 面的 handler 大多用
// `ss.Context()` 而不是入参 ctx 取上下文，所以两条都必须是派生后的那个——
// 只换入参不包装流的话，本层对流等于没生效。
func TestTimeoutStreamInterceptor_WrapsStreamContext(t *testing.T) {
	ss := &fakeStream{ctx: context.Background()}

	var argCtx, streamCtx context.Context
	if err := callStream(TimeoutStreamInterceptor(100*time.Millisecond), ss,
		func(s grpc.ServerStream) error {
			streamCtx = s.Context()
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if streamCtx == nil {
		t.Fatal("handler 未收到 stream")
	}
	dl, ok := streamCtx.Deadline()
	if !ok {
		t.Fatal("ss.Context() 应带上服务端默认截止期（包装未生效）")
	}
	if remaining := time.Until(dl); remaining <= 0 || remaining > 200*time.Millisecond {
		t.Fatalf("剩余 %v，期望接近 100ms", remaining)
	}
	if streamCtx == ss.ctx {
		t.Error("应换成派生 ctx，不能把原 ctx 直接交给 handler")
	}
	argCtx = ss.ctx // 原始流的 ctx 不该被就地改写
	if _, had := argCtx.Deadline(); had {
		t.Error("原 stream 的 ctx 被就地改动（应返回包装视图，不动原对象）")
	}
}

// TestTimeoutStreamInterceptor_NeverShortensCallerDeadline 与 unary 同口径：
// 调用方已给的预算不被腰斩，且此时不必包装流。
func TestTimeoutStreamInterceptor_NeverShortensCallerDeadline(t *testing.T) {
	orig, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	ss := &fakeStream{ctx: orig}

	var seen grpc.ServerStream
	if err := callStream(TimeoutStreamInterceptor(10*time.Millisecond), ss,
		func(s grpc.ServerStream) error { seen = s; return nil }); err != nil {
		t.Fatal(err)
	}
	dl, ok := seen.Context().Deadline()
	if !ok {
		t.Fatal("deadline 丢了")
	}
	if time.Until(dl) < 59*time.Minute {
		t.Fatalf("调用方的 1h 预算被改成 %v——本层无权缩短它", time.Until(dl))
	}
	if seen != grpc.ServerStream(ss) {
		t.Error("已有 deadline 时应原样放过，不必包装")
	}
}

// TestTimeoutStreamInterceptor_NonPositiveIsPassthrough d<=0 等同不装配：
// 既不能挂截止期，也不该多出包装层。
func TestTimeoutStreamInterceptor_NonPositiveIsPassthrough(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		ss := &fakeStream{ctx: context.Background()}
		var seen grpc.ServerStream
		if err := callStream(TimeoutStreamInterceptor(d), ss,
			func(s grpc.ServerStream) error { seen = s; return nil }); err != nil {
			t.Fatalf("d=%v err=%v", d, err)
		}
		if _, had := seen.Context().Deadline(); had {
			t.Errorf("d=%v 不应挂超时", d)
		}
		if seen != grpc.ServerStream(ss) {
			t.Errorf("d=%v 应原样传流", d)
		}
	}
}

// TestTimeoutStreamInterceptor_CancelsWhenHandlerReturns 流 handler 的返回即
// 流的结束，此时派生 ctx 必须被 cancel（defer cancel()），否则每条流泄漏一个
// deadline timer。
func TestTimeoutStreamInterceptor_CancelsWhenHandlerReturns(t *testing.T) {
	var seen context.Context
	if err := callStream(TimeoutStreamInterceptor(time.Minute), &fakeStream{ctx: context.Background()},
		func(s grpc.ServerStream) error { seen = s.Context(); return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-seen.Done():
	case <-time.After(time.Second):
		t.Fatal("handler 返回后派生 ctx 未被取消（cancel 泄漏）")
	}
}

// TestTimeoutStreamInterceptor_HandlerErrorPassesThrough 与 unary 同：不改错误语义。
func TestTimeoutStreamInterceptor_HandlerErrorPassesThrough(t *testing.T) {
	want := status.Error(codes.NotFound, "gone")
	err := callStream(TimeoutStreamInterceptor(time.Second), &fakeStream{ctx: context.Background()},
		func(grpc.ServerStream) error { return want })
	if status.Code(err) != codes.NotFound || err.Error() != want.Error() {
		t.Fatalf("err = %v，want 原样透传 %v", err, want)
	}
}
