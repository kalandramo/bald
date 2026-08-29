package grpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	berrors "github.com/kalandramo/bald/pkg/berrors"
	"github.com/kalandramo/bald/pkg/berrors/grpcerr"
)

// TestErrorInterceptor_ConvertsBizError 验证 *berrors.Error 被转成带
// Code/Message/Reason 的 gRPC status，而不是退化成空的 Unknown。
//
// 这是本拦截器存在的核心价值：没有它时，handler 返回的 *berrors.Error
// 会被 gRPC 当成普通 error，Code/Reason/Details 全部丢失。
func TestErrorInterceptor_ConvertsBizError(t *testing.T) {
	bizErr := berrors.BadRequest("EMPTY_NAME").WithMessage("name 不能为空")

	interceptor := ErrorInterceptor()
	handler := func(_ context.Context, _ any) (any, error) {
		return nil, bizErr
	}

	_, err := interceptor(context.Background(), "req", nil, handler)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument (from berrors.BadRequest)", got)
	}
	// Message 必须透传，否则调用方只看到一个空壳错误，无法定位问题。
	if msg := status.Convert(err).Message(); msg != "name 不能为空" {
		t.Errorf("message = %q, want %q", msg, "name 不能为空")
	}
	// Reason 走 ErrorInfo，可用 FromStatus 还原（验证双向闭环）。
	restored, ok := berrors.FromError(grpcerr.FromStatus(status.Convert(err)))
	if !ok {
		t.Fatalf("restored error is not *berrors.Error: %T", err)
	}
	if restored.Reason != "EMPTY_NAME" {
		t.Errorf("reason = %q, want EMPTY_NAME", restored.Reason)
	}
}

// TestErrorInterceptor_PassesThrough 验证成功路径不受影响。
func TestErrorInterceptor_PassesThrough(t *testing.T) {
	interceptor := ErrorInterceptor()
	handler := func(_ context.Context, rq any) (any, error) {
		return "resp:" + rq.(string), nil
	}

	resp, err := interceptor(context.Background(), "req", nil, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "resp:req" {
		t.Errorf("resp = %v, want resp:req", resp)
	}
}

// TestErrorInterceptor_PlainError 验证非 *berrors.Error 的原生 error 原样透传、语义不丢。
func TestErrorInterceptor_PlainError(t *testing.T) {
	plain := errors.New("boom")
	interceptor := ErrorInterceptor()
	handler := func(_ context.Context, _ any) (any, error) {
		return nil, plain
	}

	_, err := interceptor(context.Background(), "req", nil, handler)
	if err == nil {
		t.Fatal("expected error")
	}
	if msg := status.Convert(err).Message(); msg != "boom" {
		t.Errorf("message = %q, want boom", msg)
	}
}

// TestErrorInterceptor_Usable 契约测试：返回值可直接用于 ChainUnaryInterceptor。
func TestErrorInterceptor_Usable(t *testing.T) {
	var (
		_ grpc.UnaryServerInterceptor = ErrorInterceptor()
		_ grpc.ServerOption           = grpc.ChainUnaryInterceptor(ErrorInterceptor())
	)
}
