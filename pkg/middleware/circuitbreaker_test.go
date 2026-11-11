// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package middleware

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/circuitbreaker"
)

// recordingBreaker 是测试用熔断器：Allow 结果可设、Mark* 计数可查（原 gin 面
// 行为测试的伴生助手，门面裁撤后统一件是唯一实现，行为验收归位于本文件）。
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

func (r *recordingBreaker) setReject() { r.mu.Lock(); r.allowErr = errors.New("open"); r.mu.Unlock() }

func (r *recordingBreaker) counts() (allow, success, failure int32) {
	return atomic.LoadInt32(&r.allowN), atomic.LoadInt32(&r.success), atomic.LoadInt32(&r.failure)
}

// runCB 直接调统一件（不经桥——失败口径的分类在件内、与桥无关；gin 面 5xx
// 经桥哨兵到达的形态由 bundle 的 FieldsMapped 行为测试守住）。
func runCB(t *testing.T, cb circuitbreaker.CircuitBreaker, next Handler, req any) (any, error) {
	t.Helper()
	h := CircuitBreaker(cb)(next)
	resp, err := h(context.Background(), req)
	// panic 重抛由调用方的 defer/recover 处理（与 panic 用例一致）。
	return resp, err
}

// TestCircuitBreaker_ReportingPairing 上报配对契约：Allow 放行即欠一次 Mark*，
// 结果按语义码分类（IsFailureCode 集合）。
func TestCircuitBreaker_ReportingPairing(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantSuccess int32
		wantFailure int32
	}{
		{"成功", nil, 1, 0},
		{"4xx 是调用方的错，不算服务侧失败", berrors.NotFound("nope"), 1, 0},
		{"限流拒绝记成功（熔断不消费自己的拒流）", berrors.ResourceExhausted("RATE_LIMITED"), 1, 0},
		{"Internal 记失败", berrors.Internal("boom"), 0, 1},
		{"Unavailable 记失败", berrors.Unavailable("down"), 0, 1},
		{"DeadlineExceeded 记失败", berrors.DeadlineExceeded("late"), 0, 1},
		{"原生 context.Canceled 是客户端撤单，不算失败", context.Canceled, 1, 0},
		{"原生 context.DeadlineExceeded 算失败", context.DeadlineExceeded, 0, 1},
		{"其余原生错误兜 Unknown，算失败", errors.New("native"), 0, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rb := &recordingBreaker{}
			next := func(context.Context, interface{}) (interface{}, error) { return nil, tc.err }
			_, _ = runCB(t, rb, next, nil)
			allow, success, failure := rb.counts()
			if allow != 1 {
				t.Errorf("Allow = %d, want 1", allow)
			}
			if success != tc.wantSuccess || failure != tc.wantFailure {
				t.Errorf("Mark* = (success %d, failure %d), want (%d, %d)", success, failure, tc.wantSuccess, tc.wantFailure)
			}
		})
	}
}

// TestCircuitBreaker_RejectedRequestReportsNothing 被本层拒掉的请求不欠上报：
// Allow 已返回错误，再 Mark* 等于让拒绝喂养自己的失败率。
func TestCircuitBreaker_RejectedRequestReportsNothing(t *testing.T) {
	rb := &recordingBreaker{}
	rb.setReject()
	next := func(context.Context, interface{}) (interface{}, error) {
		t.Error("handler ran despite open circuit")
		return nil, nil
	}
	_, err := runCB(t, rb, next, nil)
	if err == nil {
		t.Fatal("开态应拒绝")
	}
	if got := statusOf(err); got != berrors.CodeUnavailable {
		t.Errorf("缺省拒绝码 = %v, want UNAVAILABLE", got)
	}
	if _, success, failure := rb.counts(); success != 0 || failure != 0 {
		t.Errorf("被拒请求不该上报，got (success %d, failure %d)", success, failure)
	}
	if reason := reasonOf(err); reason != "CIRCUIT_OPEN" {
		t.Errorf("Reason = %s, want CIRCUIT_OPEN", reason)
	}
}

// TestCircuitBreaker_WithOpenCode 拒绝码可由 openCode 语义声明（契约
// open_code 字段；面段 status_code 经 HTTPToCode 逆映射成它）。
func TestCircuitBreaker_WithOpenCode(t *testing.T) {
	rb := &recordingBreaker{}
	rb.setReject()
	next := func(context.Context, interface{}) (interface{}, error) { return nil, nil }
	_, err := CircuitBreaker(rb, WithOpenCode(berrors.CodeInternal))(next)(context.Background(), nil)
	if got := statusOf(err); got != berrors.CodeInternal {
		t.Errorf("openCode = %v, want INTERNAL", got)
	}
}

// TestCircuitBreaker_NilPassesThrough nil 熔断器 = 这一层透明（零开销省略）。
func TestCircuitBreaker_NilPassesThrough(t *testing.T) {
	ran := false
	next := func(context.Context, interface{}) (interface{}, error) { ran = true; return nil, nil }
	if _, err := CircuitBreaker(nil)(next)(context.Background(), nil); err != nil {
		t.Fatalf("nil breaker 应透传，got err=%v", err)
	}
	if !ran {
		t.Fatal("handler 未执行")
	}
}

// TestCircuitBreaker_PanicReportsFailureAndRethrows 上报义务的牙：panic 路径
// 必须在 defer 里记失败后原样抛出——hystrix 半开态的在途探针标记只有 Mark*
// 会复位，漏掉一次上报是熔断从此永久拒绝。
func TestCircuitBreaker_PanicReportsFailureAndRethrows(t *testing.T) {
	rb := &recordingBreaker{}
	next := func(context.Context, interface{}) (interface{}, error) { panic("boom") }
	func() {
		defer func() { _ = recover() }()
		_, _ = runCB(t, rb, next, nil)
	}()
	_, success, failure := rb.counts()
	if failure != 1 {
		t.Errorf("panic 应记失败，failure = %d", failure)
	}
	if success != 0 {
		t.Errorf("panic 不应记成功，success = %d", success)
	}
}

// TestIsFailureCode 分类集合与面段熔断的失败判定同组（对齐 grpcmw failureCodes
// 的既有口径——ResourceExhausted 刻意不在集合里）。
func TestIsFailureCode(t *testing.T) {
	for _, tc := range []struct {
		code uint32
		want bool
	}{
		{berrors.CodeInternal, true},
		{berrors.CodeUnknown, true},
		{berrors.CodeUnavailable, true},
		{berrors.CodeDeadlineExceeded, true},
		{berrors.CodeDataLoss, true},
		{berrors.CodeResourceExhausted, false},
		{berrors.CodeInvalidArgument, false},
		{berrors.CodeNotFound, false},
		{berrors.CodePermissionDenied, false},
		{berrors.CodeUnauthenticated, false},
	} {
		if got := IsFailureCode(tc.code); got != tc.want {
			t.Errorf("IsFailureCode(%d) = %v, want %v", tc.code, got, tc.want)
		}
	}
}

// statusOf/reasonOf 从错误里取语义码与 Reason（*berrors.Error 契约面）。
func statusOf(err error) uint32 {
	if e, ok := berrors.FromError(err); ok {
		return e.Code
	}
	return 0
}

func reasonOf(err error) string {
	if e, ok := berrors.FromError(err); ok {
		return e.Reason
	}
	return strings.TrimSpace("")
}
