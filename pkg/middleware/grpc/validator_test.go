package grpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
)

// TestValidatorInterceptor_InvokesCallback 验证拦截器把请求交给注入的回调，
// 且校验失败时不进入 handler。
func TestValidatorInterceptor_InvokesCallback(t *testing.T) {
	wantErr := errors.New("bad request")
	handlerCalled := false

	var gotReq any
	interceptor := ValidatorInterceptor(func(_ context.Context, rq any) error {
		gotReq = rq
		return wantErr
	})
	handler := func(_ context.Context, rq any) (any, error) {
		handlerCalled = true
		return "resp", nil
	}

	resp, err := interceptor(context.Background(), "req-body", nil, handler)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if resp != nil {
		t.Errorf("resp = %v, want nil when validation fails", resp)
	}
	if handlerCalled {
		t.Error("handler must not be called when validation fails")
	}
	if gotReq != "req-body" {
		t.Errorf("callback received %v, want req-body", gotReq)
	}
}

// TestValidatorInterceptor_PassesThrough 验证校验通过时正常进入 handler。
func TestValidatorInterceptor_PassesThrough(t *testing.T) {
	interceptor := ValidatorInterceptor(func(_ context.Context, _ any) error {
		return nil
	})
	handler := func(_ context.Context, rq any) (any, error) {
		return "ok", nil
	}

	resp, err := interceptor(context.Background(), "req", nil, handler)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp != "ok" {
		t.Errorf("resp = %v, want ok", resp)
	}
}

// TestValidatorInterceptor_NilCallback 回归防护：nil 回调是「不做校验」的显式语义，
// 拦截器应直接放行，不得 panic。
//
// 这与旧实现的缺陷形成对照：旧实现传 `validation.NewValidator(nil)` 会得到一个
// 「看似注册了、实际什么都不校验」的校验器，问题被隐藏。改造后 nil 就是 nil，
// 不挂校验这件事在调用处是可见的。
func TestValidatorInterceptor_NilCallback(t *testing.T) {
	interceptor := ValidatorInterceptor(nil)
	handler := func(_ context.Context, rq any) (any, error) {
		return "ok", nil
	}

	resp, err := interceptor(context.Background(), "req", nil, handler)
	if err != nil {
		t.Fatalf("nil callback should pass through, got err: %v", err)
	}
	if resp != "ok" {
		t.Errorf("resp = %v, want ok", resp)
	}
}

// TestValidatorInterceptor_NotBoundToAnyLibrary 契约防护：
// 拦截器签名必须保持「接受函数回调」的形态，不得反向依赖具体校验库。
// 这里用最简单的函数字面量即可工作，证明核心包未被任何实现绑定。
func TestValidatorInterceptor_NotBoundToAnyLibrary(t *testing.T) {
	var (
		_ grpc.UnaryServerInterceptor = ValidatorInterceptor(nil)
		_ grpc.UnaryServerInterceptor = ValidatorInterceptor(func(context.Context, any) error { return nil })
	)
}
