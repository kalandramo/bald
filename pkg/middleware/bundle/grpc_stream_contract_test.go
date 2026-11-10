package bundle

// 本文件锁 §6.2 未修项②收口后的 stream 面口径：bundle 的 stream 链只在给了
// RateLimit 时出现，与 unary 共用同一个桶，错误以可还原的 code 出链；
// unary 式默认超时**不**外溢到流上。

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	ginmw "github.com/kalandramo/bald/pkg/middleware/gin"
)

// fakeStream 只提供 ctx；断言只看 Context() 与 handler 是否被触达。
type fakeStream struct {
	ctx context.Context
}

var _ grpc.ServerStream = (*fakeStream)(nil)

func (s *fakeStream) SetHeader(metadata.MD) error  { return nil }
func (s *fakeStream) SendHeader(metadata.MD) error { return nil }
func (s *fakeStream) SetTrailer(metadata.MD)       {}
func (s *fakeStream) Context() context.Context     { return s.ctx }
func (s *fakeStream) SendMsg(any) error            { return nil }
func (s *fakeStream) RecvMsg(any) error            { return nil }

// streamChainInvoke 按 grpc.ChainStreamInterceptor 的语义把链跑起来
// （slice 序即外→内，与 grpc 内部组链的方式一致）。
func streamChainInvoke(t *testing.T, ics []grpc.StreamServerInterceptor, ss grpc.ServerStream,
	handler func(grpc.ServerStream) error,
) error {
	t.Helper()
	h := func(_ any, s grpc.ServerStream) error { return handler(s) }
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/Stream"}
	for i := len(ics) - 1; i >= 0; i-- {
		ic := ics[i]
		next := h
		h = func(srv any, s grpc.ServerStream) error { return ic(srv, s, info, next) }
	}
	return h(nil, ss)
}

// TestGRPCStreamChain_RateLimitRejectIsReadable 给了 RateLimit 后，配额耗尽的第三条流
// 必须以 RESOURCE_EXHAUSTED 出链——不是 Unknown。这条同时钉住两件事：限流层确实在
// 链上，且 Error 收口层确实在它**外侧**（把 ErrorStreamInterceptor 从链首删掉，
// 这里立刻退化成 Unknown，调用方看不出自己是「被限流」还是「服务坏了」）。
func TestGRPCStreamChain_RateLimitRejectIsReadable(t *testing.T) {
	rl, _, err := RateLimitFromMiddleware(mwWithRateLimit(0.0001, 2, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	b := New(Public(), RateLimit(rl, false))
	ics := b.GRPCStreamInterceptors()
	if len(ics) != 2 {
		t.Fatalf("stream 链应为 Error+RateLimit 两层，len=%d", len(ics))
	}

	var hits int
	var codesSeen []codes.Code
	for i := 0; i < 3; i++ {
		e := streamChainInvoke(t, ics, &fakeStream{ctx: context.Background()},
			func(grpc.ServerStream) error { hits++; return nil })
		codesSeen = append(codesSeen, status.Code(e))
	}
	want := []codes.Code{codes.OK, codes.OK, codes.ResourceExhausted}
	if !equal(codesName(codesSeen), codesName(want)) {
		t.Fatalf("流建立序列 = %v, want %v", codesSeen, want)
	}
	if hits != 2 {
		t.Fatalf("被拒的流不该触达 handler，hits=%d want 2", hits)
	}
}

// TestGRPCStreamChain_ShareBucketWithUnary 两条链共用一个桶是本文件的核心：
// unary 用光配额后流建不起来，反之亦然。若实现改成各链各建 limiter，两条断言
// 都会红。
func TestGRPCStreamChain_ShareBucketWithUnary(t *testing.T) {
	rl, _, err := RateLimitFromMiddleware(mwWithRateLimit(0.0001, 1, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	b := New(Public(), RateLimit(rl, false))
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}

	// unary 先吃掉唯一的令牌。
	if _, err := chainInvoke(b.GRPCInterceptors(), info,
		func(context.Context, any) (any, error) { return "ok", nil }, authedCtx(), nil); err != nil {
		t.Fatalf("首个 unary 应放行: %v", err)
	}
	// 同一条 bundle 的 stream 链必须看到桶已空。
	var called bool
	err = streamChainInvoke(t, b.GRPCStreamInterceptors(), &fakeStream{ctx: context.Background()},
		func(grpc.ServerStream) error { called = true; return nil })
	if called {
		t.Fatal("配额已被 unary 用掉，流不该建立")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code = %v, want ResourceExhausted (err=%v)", status.Code(err), err)
	}

	// 反向：新桶里先建一条流，再打 unary 应被拒。
	rl2, _, err := RateLimitFromMiddleware(mwWithRateLimit(0.0001, 1, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl2.Close() }()
	b2 := New(Public(), RateLimit(rl2, false))
	if err := streamChainInvoke(t, b2.GRPCStreamInterceptors(), &fakeStream{ctx: context.Background()},
		func(grpc.ServerStream) error { return nil }); err != nil {
		t.Fatalf("首条流应放行: %v", err)
	}
	if _, err := chainInvoke(b2.GRPCInterceptors(), info,
		func(context.Context, any) (any, error) {
			t.Error("配额已被流用掉，unary 不该触达 handler")
			return nil, nil
		}, authedCtx(), nil); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code = %v, want ResourceExhausted (err=%v)", status.Code(err), err)
	}
}

// TestGRPCChain_StreamOptionAppearsOnlyWithRateLimit 零配置时 GRPCChain 的选项数
// 必须与收口②之前完全一致（现有部署二进制兼容）；给了 RateLimit 才多一个
// ChainStreamInterceptor。
func TestGRPCChain_StreamOptionAppearsOnlyWithRateLimit(t *testing.T) {
	if got := len(New(Public()).GRPCChain()); got != 1 {
		t.Fatalf("未配置时 GRPCChain 应只有 1 个选项，got %d", got)
	}
	if ics := New(Public(), Timeout(&ginmw.TimeoutConfig{DefaultTimeoutMs: 40})).GRPCStreamInterceptors(); len(ics) != 0 {
		t.Fatalf("没给 RateLimit 时不该出 stream 链，len=%d", len(ics))
	}

	rl, _, err := RateLimitFromMiddleware(mwWithRateLimit(0.0001, 5, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()
	if got := len(New(Public(), RateLimit(rl, false)).GRPCChain()); got != 2 {
		t.Fatalf("给了 RateLimit 应有 2 个选项（unary + stream），got %d", got)
	}
}

// TestGRPCStreamChain_UnaryTimeoutDoesNotLeakOntoStreams 记录在案的口径：
// bundle.Timeout 只驱动 unary 链。流 handler 的生命周期就是整条流，把同一个 d
// 套上去会把它静默变成「流的最大存活时长」，长订阅到点被砍且错误不可区分。
// 这条断言的作用是让「顺手把超时也加进 stream 链」这个改动必须先过一道显式的
// 决策，而不是作为对称美学被合进来。
func TestGRPCStreamChain_UnaryTimeoutDoesNotLeakOntoStreams(t *testing.T) {
	rl, _, err := RateLimitFromMiddleware(mwWithRateLimit(0.0001, 5, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	b := New(Public(), RateLimit(rl, false), Timeout(&ginmw.TimeoutConfig{DefaultTimeoutMs: 20}))

	// 对照：unary 侧同一对 Option 确实挂了超时层（放行调用可见截止期）。
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	var unaryDeadline bool
	if _, err := chainInvoke(b.GRPCInterceptors(), info,
		func(c context.Context, _ any) (any, error) {
			_, unaryDeadline = c.Deadline()
			return "ok", nil
		}, authedCtx(), nil); err != nil {
		t.Fatal(err)
	}
	if !unaryDeadline {
		t.Fatal("对照用例失效：unary 侧应有超时层")
	}

	var sawDeadline bool
	if err := streamChainInvoke(t, b.GRPCStreamInterceptors(), &fakeStream{ctx: context.Background()},
		func(s grpc.ServerStream) error {
			_, sawDeadline = s.Context().Deadline()
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if sawDeadline {
		t.Fatal("stream 链不该带上 unary 式默认超时（见本用例注释的口径）")
	}
}
