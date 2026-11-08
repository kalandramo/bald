package resiliency

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/circuitbreaker"
	"github.com/kalandramo/bald/retry"
)

// fakeBreaker 是可断言上报计数的熔断器替身：状态/放行错误可设，Mark* 计数。
type fakeBreaker struct {
	mu       sync.Mutex
	allowErr error
	success  int
	failure  int
}

func (b *fakeBreaker) Allow() error { return b.allowErr }
func (b *fakeBreaker) MarkSuccess() { b.mu.Lock(); b.success++; b.mu.Unlock() }
func (b *fakeBreaker) MarkFailure() { b.mu.Lock(); b.failure++; b.mu.Unlock() }
func (b *fakeBreaker) Execute(ctx context.Context, fn func() error) error {
	if err := b.Allow(); err != nil {
		return err
	}
	err := fn()
	if err != nil {
		b.MarkFailure()
	} else {
		b.MarkSuccess()
	}
	return err
}
func (b *fakeBreaker) State() circuitbreaker.State { return circuitbreaker.StateClosed }
func (b *fakeBreaker) Close() error                { return nil }

func (b *fakeBreaker) counts() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.success, b.failure
}

// invoke 直接调拦截器（fake invoker，无需真服务）：cc/req/reply 均可 nil。
func invoke(ic grpc.UnaryClientInterceptor, invoker grpc.UnaryInvoker) error {
	return ic(context.Background(), "/test.Svc/Method", nil, nil, nil, invoker)
}

func okInvoker(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
	return nil
}

func statusInvoker(code codes.Code) grpc.UnaryInvoker {
	return func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		return status.Error(code, "downstream said so")
	}
}

func fastRetrier(attempts int) *retry.Retrier {
	return retry.New(
		retry.WithMaxAttempts(attempts),
		retry.WithBackoff(retry.FixedBackoff(time.Millisecond)),
	)
}

func TestGRPCSuccessMarksSuccess(t *testing.T) {
	b := &fakeBreaker{}
	if err := invoke(UnaryClientInterceptor(Policy{Breaker: b}), okInvoker); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if s, f := b.counts(); s != 1 || f != 0 {
		t.Fatalf("counts=(%d,%d), want (1,0)", s, f)
	}
}

func TestGRPCServiceFailureMarksFailure(t *testing.T) {
	for _, code := range []codes.Code{codes.Internal, codes.Unknown, codes.Unavailable, codes.DeadlineExceeded, codes.DataLoss} {
		b := &fakeBreaker{}
		_ = invoke(UnaryClientInterceptor(Policy{Breaker: b}), statusInvoker(code))
		if s, f := b.counts(); s != 0 || f != 1 {
			t.Fatalf("code %v: counts=(%d,%d), want (0,1)", code, s, f)
		}
	}
}

// 客户端侧错误码与 ResourceExhausted 不计失败：坏请求/下游节流不该替全体开窗。
func TestGRPCClientErrorAndThrottleMarkSuccess(t *testing.T) {
	for _, code := range []codes.Code{codes.InvalidArgument, codes.NotFound, codes.PermissionDenied, codes.ResourceExhausted} {
		b := &fakeBreaker{}
		_ = invoke(UnaryClientInterceptor(Policy{Breaker: b}), statusInvoker(code))
		if s, f := b.counts(); s != 1 || f != 0 {
			t.Fatalf("code %v: counts=(%d,%d), want (1,0)", code, s, f)
		}
	}
}

// 开态：Allow 拒绝即返回 ErrCircuitOpen，invoker 不被调、不欠上报。
func TestGRPCOpenCircuitSkipsInvoker(t *testing.T) {
	b := &fakeBreaker{allowErr: circuitbreaker.ErrCircuitOpen}
	called := false
	invoker := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		called = true
		return nil
	}
	err := invoke(UnaryClientInterceptor(Policy{Breaker: b}), invoker)
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("err=%v, want ErrCircuitOpen", err)
	}
	if called {
		t.Fatal("invoker called despite open circuit")
	}
	if s, f := b.counts(); s != 0 || f != 0 {
		t.Fatalf("open circuit reported (%d,%d), want (0,0)", s, f)
	}
}

// 熔断在外、重试在内：重试后成功的逻辑请求记一次成功（不是每次 attempt 各记）。
func TestGRPCRetryThenSuccessMarksOneSuccess(t *testing.T) {
	b := &fakeBreaker{}
	calls := 0
	invoker := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		calls++
		if calls < 3 {
			return status.Error(codes.Unavailable, "transient")
		}
		return nil
	}
	p := Policy{Breaker: b, Retrier: fastRetrier(5)}
	if err := invoke(UnaryClientInterceptor(p), invoker); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if calls != 3 {
		t.Fatalf("invoker called %d times, want 3", calls)
	}
	if s, f := b.counts(); s != 1 || f != 0 {
		t.Fatalf("counts=(%d,%d), want (1,0) — one logical success", s, f)
	}
}

// 重试耗尽：最终失败记一次失败，错误包 ErrMaxAttempts，attempt 数=maxAttempts。
func TestGRPCRetryExhaustedMarksOneFailure(t *testing.T) {
	b := &fakeBreaker{}
	calls := 0
	invoker := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		calls++
		return status.Error(codes.Unavailable, "still down")
	}
	p := Policy{Breaker: b, Retrier: fastRetrier(3)}
	err := invoke(UnaryClientInterceptor(p), invoker)
	if !errors.Is(err, retry.ErrMaxAttempts) {
		t.Fatalf("err=%v, want ErrMaxAttempts", err)
	}
	if calls != 3 {
		t.Fatalf("invoker called %d times, want 3", calls)
	}
	if s, f := b.counts(); s != 0 || f != 1 {
		t.Fatalf("counts=(%d,%d), want (0,1) — one logical failure", s, f)
	}
}

// 不可重试错误（分类器拒绝）立即返回，不重试：默认 RetryAny 会重试，故用
// 自定义分类器只重试 Unavailable，InvalidArgument 立即失败且记成功（客户端错）。
func TestGRPCNonRetryableReturnsImmediately(t *testing.T) {
	b := &fakeBreaker{}
	calls := 0
	invoker := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		calls++
		return status.Error(codes.InvalidArgument, "bad request")
	}
	r := retry.New(retry.WithMaxAttempts(5), retry.WithBackoff(retry.FixedBackoff(time.Millisecond)),
		retry.WithClassifier(func(err error) bool { return status.Code(err) == codes.Unavailable }))
	err := invoke(UnaryClientInterceptor(Policy{Breaker: b, Retrier: r}), invoker)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err=%v, want InvalidArgument", err)
	}
	if calls != 1 {
		t.Fatalf("invoker called %d times, want 1 (non-retryable)", calls)
	}
	if s, f := b.counts(); s != 1 || f != 0 {
		t.Fatalf("counts=(%d,%d), want (1,0)", s, f)
	}
}

// panic 路径：记一次失败后原样抛出（半开探针不悬死）。
func TestGRPCPanicMarksFailureAndRethrows(t *testing.T) {
	b := &fakeBreaker{}
	ic := UnaryClientInterceptor(Policy{Breaker: b})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic to propagate")
		}
		if s, f := b.counts(); s != 0 || f != 1 {
			t.Fatalf("panic counts=(%d,%d), want (0,1)", s, f)
		}
	}()
	_ = invoke(ic, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		panic("boom")
	})
}

// 零值 Policy 直通：不熔断、不重试，错误原样返回。
func TestGRPCZeroPolicyPassthrough(t *testing.T) {
	calls := 0
	invoker := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		calls++
		return status.Error(codes.Unavailable, "down")
	}
	err := invoke(UnaryClientInterceptor(Policy{}), invoker)
	if status.Code(err) != codes.Unavailable || calls != 1 {
		t.Fatalf("err=%v calls=%d, want Unavailable once", err, calls)
	}
}

func TestDefaultGRPCFailureWalksJoinedTree(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain unavailable", status.Error(codes.Unavailable, "x"), true},
		{"plain invalid", status.Error(codes.InvalidArgument, "x"), false},
		{"joined with unavailable", errors.Join(retry.ErrMaxAttempts, status.Error(codes.Unavailable, "x")), true},
		{"joined with invalid", errors.Join(retry.ErrMaxAttempts, status.Error(codes.InvalidArgument, "x")), false},
		{"non-status error", errors.New("plain"), false},
		{"joined non-status", errors.Join(retry.ErrMaxAttempts, errors.New("plain")), false},
		{"nested wrap", errors.Join(retry.ErrMaxAttempts, errors.Join(errors.New("a"), status.Error(codes.DataLoss, "x"))), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DefaultGRPCFailure(c.err); got != c.want {
				t.Fatalf("DefaultGRPCFailure(%v)=%v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestDefaultHTTPFailure(t *testing.T) {
	cases := []struct {
		name string
		resp *http.Response
		err  error
		want bool
	}{
		{"transport error", nil, errors.New("conn refused"), true},
		{"500", &http.Response{StatusCode: 500}, nil, true},
		{"503", &http.Response{StatusCode: 503}, nil, true},
		{"429 not failure", &http.Response{StatusCode: 429}, nil, false},
		{"404 not failure", &http.Response{StatusCode: 404}, nil, false},
		{"200", &http.Response{StatusCode: 200}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DefaultHTTPFailure(c.resp, c.err); got != c.want {
				t.Fatalf("=%v, want %v", got, c.want)
			}
		})
	}
}

func TestDefaultHTTPRetry(t *testing.T) {
	retryable := []int{429, 500, 502, 503, 504}
	for _, code := range retryable {
		if !DefaultHTTPRetry(&http.Response{StatusCode: code}, nil) {
			t.Fatalf("status %d should be retryable", code)
		}
	}
	for _, code := range []int{200, 301, 400, 404, 501, 505} {
		if DefaultHTTPRetry(&http.Response{StatusCode: code}, nil) {
			t.Fatalf("status %d should not be retryable", code)
		}
	}
	if !DefaultHTTPRetry(nil, errors.New("conn refused")) {
		t.Fatal("transport error should be retryable")
	}
}