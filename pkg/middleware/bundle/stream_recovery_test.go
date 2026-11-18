package bundle

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeServerStream 是链执行的最小 ServerStream 桩：Recovery 基线只经
// StreamServerInterceptor 桥取 ctx 构造 ServerContext，不触达收发方法。
type fakeServerStream struct {
	grpc.ServerStream
}

func (f *fakeServerStream) Context() context.Context  { return context.Background() }
func (f *fakeServerStream) SendMsg(interface{}) error { return nil }
func (f *fakeServerStream) RecvMsg(interface{}) error { return nil }

// TestGRPCStreamInterceptors_RecoveryBaseline 复现 AR-5：未启用统一段时
// stream 链没有任何 recovery——stream handler 一次 panic 整个进程退出
// （grpc-go 不 recover）。修复后 Recovery 基线无条件在链头，panic 被兜住
// 转成 Internal status（带 berrors 的 Code/Reason，经 Stream 桥 grpcerr 收口）。
func TestGRPCStreamInterceptors_RecoveryBaseline(t *testing.T) {
	b := New(Public()) // 无统一段、无限流、无熔断
	chain := b.GRPCStreamInterceptors()
	if len(chain) == 0 {
		t.Fatal("stream 链应至少有 Recovery 基线（修复前为空链，AR-5）")
	}

	// 手工按外→内组链执行（ChainStreamInterceptor 返回 ServerOption 不可直调）：
	// chain[0] 最外层，逐层内包，最内层是 panic 的 handler。
	info := &grpc.StreamServerInfo{FullMethod: "/demo.Svc/Stream", IsClientStream: true, IsServerStream: true}
	inner := func(_ interface{}, _ grpc.ServerStream) error {
		panic("boom-stream")
	}
	for i := len(chain) - 1; i >= 0; i-- {
		ic := chain[i]
		next := inner
		inner = func(srv interface{}, ss grpc.ServerStream) error {
			return ic(srv, ss, info, next)
		}
	}

	// panic 不允许逃出链（逃出即进程崩溃）；链内被兜住转为 Internal。
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic 逃出 stream 拦截器链（进程将崩溃）: %v", r)
			}
		}()
		err = inner(nil, &fakeServerStream{})
	}()

	if err == nil {
		t.Fatal("panic 应被 Recovery 兜住转为错误")
	}
	if !strings.Contains(err.Error(), "boom-stream") {
		t.Fatalf("错误应携带 panic 原值，got: %v", err)
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Internal {
		t.Fatalf("panic 应转为 Internal status, got: %v", err)
	}
}
