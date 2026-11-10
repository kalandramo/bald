package grpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/berrors/grpcerr"
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

// TestErrorStreamInterceptor_ConvertsBizError stream 面同样必须收口：
// *berrors.Error 没实现 GRPCStatus()，grpc-go 对流返回错误做 status.Convert 时
// 会把它兜成 Unknown，Code/Reason 全丢。这条断的是 RESOURCE_EXHAUSTED +
// Reason 真的到了客户端——限流层（RateLimitStreamInterceptor）的拒绝语义完全
// 依赖它，否则「被限流」和「服务坏了」在调用方眼里一模一样。
func TestErrorStreamInterceptor_ConvertsBizError(t *testing.T) {
	bizErr := berrors.ResourceExhausted("RATE_LIMITED").WithMessage("grpc: rate limit exceeded")

	err := callStream(ErrorStreamInterceptor(), &fakeStream{ctx: context.Background()},
		func(grpc.ServerStream) error { return bizErr })
	if err == nil {
		t.Fatal("expected error")
	}
	if got := status.Code(err); got != codes.ResourceExhausted {
		t.Fatalf("code = %v, want ResourceExhausted（收口未生效即退化成 Unknown）", got)
	}
	recovered := grpcerr.FromStatus(status.Convert(err))
	berr, ok := berrors.FromError(recovered)
	if !ok || berr.Reason != "RATE_LIMITED" {
		t.Fatalf("应还原出 Reason=%s，got %+v (err=%v)", "RATE_LIMITED", berr, err)
	}
}

// TestErrorStreamInterceptor_PassThroughNilAndPlain 与 unary 同口径：nil 原样
// nil；已是 gRPC status 的错误 **保住自己的 code**（这是「不丢语义」的实质）；
// 原生 error 的文本仍在（`grpcerr.ToStatus` 对非 *berrors.Error 走
// `status.Convert`，落 Unknown——这与 grpc-go 自己的兜底一致，不是本层新增的损失）。
func TestErrorStreamInterceptor_PassThroughNilAndPlain(t *testing.T) {
	if err := callStream(ErrorStreamInterceptor(), &fakeStream{ctx: context.Background()},
		func(grpc.ServerStream) error { return nil }); err != nil {
		t.Fatalf("nil 应原样返回 nil, got %v", err)
	}

	want := status.Error(codes.NotFound, "gone")
	err := callStream(ErrorStreamInterceptor(), &fakeStream{ctx: context.Background()},
		func(grpc.ServerStream) error { return want })
	if status.Code(err) != codes.NotFound {
		t.Fatalf("已是 status 的错误应保住 code：got %v (err=%v)", status.Code(err), err)
	}
	if msg := status.Convert(err).Message(); msg != "gone" {
		t.Errorf("message = %q, want gone", msg)
	}

	plain := errors.New("boom")
	err = callStream(ErrorStreamInterceptor(), &fakeStream{ctx: context.Background()},
		func(grpc.ServerStream) error { return plain })
	if msg := status.Convert(err).Message(); msg != "boom" {
		t.Fatalf("原生 error 的文本不该丢, got %q", msg)
	}
}

// TestErrorStreamInterceptor_Usable 契约测试：可直接用于 ChainStreamInterceptor。
func TestErrorStreamInterceptor_Usable(t *testing.T) {
	var (
		_ grpc.StreamServerInterceptor = ErrorStreamInterceptor()
		_ grpc.ServerOption            = grpc.ChainStreamInterceptor(ErrorStreamInterceptor())
	)
}

// fakeStream 是最小的 grpc.ServerStream：只提供 ctx 与收发计数（原 timeout/
// rate-limit 测试的伴生助手，批次 4 物理移除后归位于本文件——stream 收口测试
// 的最小载体）。
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
