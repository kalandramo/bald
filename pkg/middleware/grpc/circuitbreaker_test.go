package grpc

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/berrors/grpcerr"
	"github.com/kalandramo/bald/circuitbreaker"
	"github.com/kalandramo/bald/circuitbreaker/hystrix"
)

// recordingCB 记录 Allow/Mark* 次数并可注入拒绝。真算法的行为在 hystrix 自己的
// 测试里覆盖；这里要钉的是**拦截器这一层的口径**（哪个 code 算失败、拒绝的形态、
// 漏上报的后果），与实现无关的假对象才判得准。
type recordingCB struct {
	mu       sync.Mutex
	allowErr error
	allowN   int32
	success  int32
	failure  int32
}

var _ circuitbreaker.CircuitBreaker = (*recordingCB)(nil)

func (r *recordingCB) Allow() error {
	atomic.AddInt32(&r.allowN, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allowErr
}

func (r *recordingCB) MarkSuccess() { atomic.AddInt32(&r.success, 1) }
func (r *recordingCB) MarkFailure() { atomic.AddInt32(&r.failure, 1) }
func (r *recordingCB) State() circuitbreaker.State {
	return circuitbreaker.StateClosed
}
func (r *recordingCB) Close() error { return nil }

func (r *recordingCB) Execute(ctx context.Context, fn func() error) error {
	if err := r.Allow(); err != nil {
		return err
	}
	if err := fn(); err != nil {
		r.MarkFailure()
		return err
	}
	r.MarkSuccess()
	return nil
}

func (r *recordingCB) reject() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.allowErr = circuitbreaker.ErrCircuitOpen
}

func (r *recordingCB) counts() (allow, success, failure int32) {
	return atomic.LoadInt32(&r.allowN), atomic.LoadInt32(&r.success), atomic.LoadInt32(&r.failure)
}

// runBreakered 按 bundle 的链序（Error 最外 → 熔断 → handler）跑一次调用，返回
// 收口后的 error 与 handler 是否执行。套上 ErrorInterceptor 是为了测到真实形态：
// 拒绝的 Code/Reason 必须能穿过收口层还原（不是被兜成 Unknown）。
func runBreakered(t *testing.T, cb circuitbreaker.CircuitBreaker, handler func(context.Context, any) (any, error)) (error, bool) {
	t.Helper()
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	var handlerCalled bool
	_, err := ErrorInterceptor()(context.Background(), nil, info,
		func(ctx context.Context, rq any) (any, error) {
			return CircuitBreakerInterceptor(cb)(ctx, rq, info, func(c context.Context, q any) (any, error) {
				handlerCalled = true
				return handler(c, q)
			})
		})
	return err, handlerCalled
}

// --- 失败口径：服务侧的 code 记失败，客户端侧的记成功 ---

func TestCircuitBreakerInterceptor_FailureClassification(t *testing.T) {
	tests := []struct {
		name        string
		handlerErr  error
		wantFailure int32
	}{
		{"nil → 成功", nil, 0},
		{"Internal → 失败", status.Error(codes.Internal, "boom"), 1},
		{"Unknown → 失败", status.Error(codes.Unknown, "?"), 1},
		{"Unavailable → 失败", status.Error(codes.Unavailable, "down"), 1},
		{"DeadlineExceeded → 失败", status.Error(codes.DeadlineExceeded, "late"), 1},
		{"DataLoss → 失败", status.Error(codes.DataLoss, "lost"), 1},
		{"InvalidArgument → 成功（调用方的错）", status.Error(codes.InvalidArgument, "bad arg"), 0},
		{"NotFound → 成功", status.Error(codes.NotFound, "gone"), 0},
		{"PermissionDenied → 成功", status.Error(codes.PermissionDenied, "deny"), 0},
		{"Unauthenticated → 成功", status.Error(codes.Unauthenticated, "anon"), 0},
		{"Canceled → 成功（客户端主动撤）", status.Error(codes.Canceled, "cancel"), 0},
		{"ResourceExhausted → 成功（那是限流的答案，不是服务坏了）",
			status.Error(codes.ResourceExhausted, "limited"), 0},
		// berrors 形态（未收口的原生错误）—— 这一组是本层最容易写错的地方。
		{"berrors.Internal → 失败", berrors.Internal("X").WithMessage("boom"), 1},
		{"berrors.Unavailable → 失败", berrors.Unavailable("X").WithMessage("down"), 1},
		{"berrors.PermissionDenied → 成功（不能兜成 Unknown 算失败）",
			berrors.PermissionDenied("DENY").WithMessage("no"), 0},
		{"berrors.NotFound → 成功", berrors.NotFound("NF").WithMessage("gone"), 0},
		{"berrors.ResourceExhausted → 成功", berrors.ResourceExhausted("RL").WithMessage("limited"), 0},
		{"berrors 包着 cause → 仍按 berrors 的 Code 判",
			berrors.BadRequest("BAD").WithCause(errors.New("原生错误")), 0},
		{"handler 交出 ctx.Err()（超时那条路）→ 失败", context.DeadlineExceeded, 1},
		{"原生 error（非 status 非 berrors）→ Unknown → 失败", errors.New("raw"), 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cb := &recordingCB{}
			err, called := runBreakered(t, cb, func(context.Context, any) (any, error) {
				return nil, tc.handlerErr
			})
			if !called {
				t.Fatal("handler 应被放行执行")
			}
			if tc.handlerErr != nil && err == nil {
				t.Fatal("handler 的错误不该被吞掉")
			}
			_, success, failure := cb.counts()
			if failure != tc.wantFailure {
				t.Errorf("MarkFailure = %d, want %d（MarkSuccess = %d）", failure, tc.wantFailure, success)
			}
			if want := int32(1) - tc.wantFailure; success != want {
				t.Errorf("MarkSuccess = %d, want %d", success, want)
			}
		})
	}
}

// TestIsFailureStatus_CoversBothErrorShapes 单独钉导出的口径函数：三种错误形态
// （gRPC status / berrors / 原生）都给出与拦截器一致的答案。
func TestIsFailureStatus_CoversBothErrorShapes(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{status.Error(codes.Internal, "x"), true},
		{status.Error(codes.InvalidArgument, "x"), false},
		{berrors.Internal("I"), true},
		{berrors.Unauthenticated("U"), false},
		{errors.New("原生"), true},  // → Unknown → 服务侧
		{context.Canceled, false}, // → Canceled：客户端撤单
	}
	for _, tc := range cases {
		if got := IsFailureStatus(tc.err); got != tc.want {
			t.Errorf("IsFailureStatus(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// --- 拒绝形态 ---

func TestCircuitBreakerInterceptor_RejectionShape(t *testing.T) {
	cb := &recordingCB{}
	cb.reject()
	err, called := runBreakered(t, cb, func(context.Context, any) (any, error) {
		return "should not run", nil
	})
	if called {
		t.Error("开态不该执行 handler")
	}
	if err == nil {
		t.Fatal("开态必须报错")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Errorf("status code = %v, want Unavailable", got)
	}
	berr, ok := berrors.FromError(grpcerr.FromStatus(status.Convert(err)))
	if !ok {
		t.Fatalf("拒绝形态没还原成 *berrors.Error：%T", err)
	}
	if berr.Reason != ReasonCircuitOpen {
		t.Errorf("reason = %q, want %q", berr.Reason, ReasonCircuitOpen)
	}
	if _, success, failure := cb.counts(); success != 0 || failure != 0 {
		t.Errorf("被拒请求不该上报，got (success %d, failure %d)", success, failure)
	}
}

func TestCircuitBreakerInterceptor_NilBreakerPassesThrough(t *testing.T) {
	err, called := runBreakered(t, nil, func(context.Context, any) (any, error) {
		return "ok", nil
	})
	if !called || err != nil {
		t.Fatalf("nil 熔断器应透明：called=%v err=%v", called, err)
	}
}

// --- panic 路径：漏上报的判据（与 gin 侧同一条契约义务） ---

func TestCircuitBreakerInterceptor_PanicReportsFailureAndRethrows(t *testing.T) {
	cb := &recordingCB{}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = CircuitBreakerInterceptor(cb)(context.Background(), nil, info,
			func(context.Context, any) (any, error) { panic("boom") })
	}()
	if recovered == nil {
		t.Fatal("panic 必须原样外抛（外层 RecoveryInterceptor 负责转 Internal）")
	}
	if _, _, failure := cb.counts(); failure != 1 {
		t.Fatalf("panic 路径必须记一次失败，got %d —— 漏上报会让半开探针永不复位", failure)
	}
}

// --- 真 hystrix：打满失败率后拒绝 ---

func TestCircuitBreakerInterceptor_TripsAgainstRealBreaker(t *testing.T) {
	cb := newHystrixForTest(t, 2, 60_000)
	defer func() { _ = cb.Close() }()

	for i := 0; i < 2; i++ {
		err, called := runBreakered(t, cb, func(context.Context, any) (any, error) {
			return nil, status.Error(codes.Internal, "boom")
		})
		if !called {
			t.Fatalf("第 %d 次不该被拒", i+1)
		}
		if got := status.Code(err); got != codes.Internal {
			t.Fatalf("第 %d 次应原样回 Internal，got %v", i+1, got)
		}
	}

	err, called := runBreakered(t, cb, func(context.Context, any) (any, error) {
		return "ok", nil
	})
	if called {
		t.Error("开窗后不该进 handler")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Errorf("第三次应 UNAVAILABLE，got %v", got)
	}
}

// TestCircuitBreakerInterceptor_ClientErrorsNeverTrip 全是客户端侧错误时永不开窗。
func TestCircuitBreakerInterceptor_ClientErrorsNeverTrip(t *testing.T) {
	cb := newHystrixForTest(t, 2, 60_000)
	defer func() { _ = cb.Close() }()

	for i := 0; i < 20; i++ {
		err, _ := runBreakered(t, cb, func(context.Context, any) (any, error) {
			return nil, berrors.PermissionDenied("DENY").WithMessage("no")
		})
		if got := status.Code(err); got != codes.PermissionDenied {
			t.Fatalf("第 %d 次应仍是 PermissionDenied，got %v", i+1, got)
		}
	}
	if st := cb.State(); st != circuitbreaker.StateClosed {
		t.Errorf("20 次拒权后仍应 Closed，state=%v —— 客户端错误被记成了服务侧失败", st)
	}
}

// newHystrixForTest 造一个极值熔断器：样本量 volume、开窗 60s（测试期间恒开）、
// 滑窗 10s（桶不旋转、计数不丢）。阈值用 hystrix 默认 0.5。
func newHystrixForTest(t *testing.T, volume int, openMs int64) circuitbreaker.CircuitBreaker {
	t.Helper()
	return hystrix.New(
		hystrix.WithRequestVolumeThreshold(volume),
		hystrix.WithSleepWindow(time.Duration(openMs)*time.Millisecond),
		hystrix.WithWindow(10*time.Second),
	)
}
