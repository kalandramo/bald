package bootstrap

// grpc_unary_func_test.go —— WithGRPCUnaryFunc 的合并顺序与延迟求值语义。
//
// 背景（W1 能力）：WithGRPCUnary 的变参在调用点即求值，若选项构造依赖运行期资源
// （认证器/授权器），构造期只能拿到 nil。WithGRPCUnaryFunc 收工厂函数，在 provider
// 闭包内（Run 期、server 构造时）调用。
//
// 本文件锁两条不变量：
//  1. 延迟工厂在 **provider 调用时**执行（而非注册 Option 时）——这是能力成立的前提；
//  2. 合并顺序为「静态 unary 在前、延迟在后」（链序是安全策略，顺序不可漂移）。

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// TestGrpcServerProvider_UnaryFuncDeferred 断言延迟工厂**不在**注册 Option 时执行，
// 而在 provider 闭包被调用时执行。
func TestGrpcServerProvider_UnaryFuncDeferred(t *testing.T) {
	var called atomic.Bool
	opt := WithGRPCUnaryFunc(func() []grpc.ServerOption {
		called.Store(true)
		return nil
	})

	if called.Load() {
		t.Fatal("WithGRPCUnaryFunc 在注册时就调用了工厂；应在 provider 调用时（Run 期）才调用")
	}

	cfg := &bootstrapv1.Server{Grpc: &bootstrapv1.Server_Grpc{Addr: ":0"}}
	srv, closer, err := GrpcServerProvider(opt)(context.Background(), cfg)
	if err != nil || srv == nil {
		t.Fatalf("provider = (%v, %v), want non-nil", srv, err)
	}
	if closer != nil {
		closer()
	}

	if !called.Load() {
		t.Fatal("provider 调用后工厂仍未执行；延迟求值未生效")
	}
}

// TestGrpcServerProvider_UnaryFuncNotCalledWhenUnconfigured 契约无 Grpc 段时返回
// nil server，此时**不应**调用延迟工厂（无 server 则无选项需求，避免副作用）。
func TestGrpcServerProvider_UnaryFuncNotCalledWhenUnconfigured(t *testing.T) {
	var called atomic.Bool
	opt := WithGRPCUnaryFunc(func() []grpc.ServerOption {
		called.Store(true)
		return nil
	})

	srv, closer, err := GrpcServerProvider(opt)(context.Background(), &bootstrapv1.Server{})
	if err != nil || srv != nil || closer != nil {
		t.Fatalf("unconfigured = (srv=%v, closer!=nil:%t, err=%v), want all nil", srv, closer != nil, err)
	}
	if called.Load() {
		t.Fatal("无 grpc 契约段时不应调用延迟工厂（无消费者却产生副作用）")
	}
}

// TestMergeUnary_StaticBeforeDeferred 断言合并顺序：静态选项在**前**、延迟在**后**。
//
// 为什么用真实调用观测：`grpc.ServerOption` 是接口，测试无法构造「可比较的替身」
// （EmptyServerOption 是空结构体，所有值相等——用它写断言会得到恒真测试，曾踩此坑）。
// 故改用拦截器实际执行序：grpc 按声明序执行 unary 拦截器，故「静态链的标记先于
// 延迟链的标记」即证明合并顺序正确。
func TestMergeUnary_StaticBeforeDeferred(t *testing.T) {
	var mu sync.Mutex
	var order []string
	mark := func(s string) grpc.UnaryServerInterceptor {
		return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			mu.Lock()
			order = append(order, s)
			mu.Unlock()
			return h(ctx, req)
		}
	}
	snapshot := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), order...)
	}

	static := WithGRPCUnary(grpc.ChainUnaryInterceptor(mark("static")))
	deferred := WithGRPCUnaryFunc(func() []grpc.ServerOption {
		return []grpc.ServerOption{grpc.ChainUnaryInterceptor(mark("deferred"))}
	})

	cfg := &bootstrapv1.Server{Grpc: &bootstrapv1.Server_Grpc{Addr: ":0"}}
	srv, closer, err := GrpcServerProvider(static, deferred)(context.Background(), cfg)
	if err != nil || srv == nil {
		t.Fatalf("provider = (%v, %v), want non-nil", srv, err)
	}
	if closer != nil {
		defer closer()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Stop(stopCtx)
		stopCancel()
		cancel()
		<-done
	}()
	waitEndpoint(t, srv)

	// 真实 unary 调用（health 服务，每个 interceptor 都会被触发一次）。
	_ = grpcHealthStatus(t, srv.Endpoint())

	got := snapshot()
	if len(got) != 2 || got[0] != "static" || got[1] != "deferred" {
		t.Fatalf("interceptor order = %v, want [static deferred]（静态在前、延迟在后）", got)
	}
}
