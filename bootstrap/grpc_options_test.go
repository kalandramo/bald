package bootstrap

// grpc_options_test.go —— gRPC 服务端选项的唯一注入路径：WithGRPCUnaryFunc 工厂队列。
//
// 能力：选项构造常依赖运行期资源（认证器/授权器/仓储），故只接受工厂函数，
// 在 provider 闭包内（Run 期、server 构造时）求值；多次登记按登记顺序串成最终链。
// 链序是安全策略（ErrorInterceptor 须挂最外层），故顺序本身要被断言。
//
// TLS 不在此队列里：由框架从契约 Server_Grpc.tls 段出（见 grpc_tls_test.go）。

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// TestGrpcServerProvider_OptionFuncDeferred 断言工厂**不在**注册 Option 时执行，
// 而在 provider 闭包被调用时执行——这是「选项可读运行期资源」的前提。
func TestGrpcServerProvider_OptionFuncDeferred(t *testing.T) {
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

// TestGrpcServerProvider_OptionFuncSkippedWhenUnconfigured 契约无 Grpc 段时返回
// nil server，此时不应调用工厂（无 server 即无选项需求，避免多余副作用）。
func TestGrpcServerProvider_OptionFuncSkippedWhenUnconfigured(t *testing.T) {
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
		t.Fatal("无 grpc 契约段时不应调用选项工厂")
	}
}

// TestGrpcServerProvider_OptionOrderIsRegistrationOrder 断言最终链序 = 登记顺序。
//
// 为什么用真实调用观测：grpc.ServerOption 是接口，EmptyServerOption 是空结构体，
// 值相等——直接比较选项会得到恒真断言。故改看 unary 拦截器的实际执行序：grpc 按
// 声明序执行链，「A 的标记先于 B」即证明 A 在链首。
func TestGrpcServerProvider_OptionOrderIsRegistrationOrder(t *testing.T) {
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

	first := WithGRPCUnaryFunc(func() []grpc.ServerOption {
		return []grpc.ServerOption{grpc.ChainUnaryInterceptor(mark("first"))}
	})
	second := WithGRPCUnaryFunc(func() []grpc.ServerOption {
		return []grpc.ServerOption{grpc.ChainUnaryInterceptor(mark("second"))}
	})

	cfg := &bootstrapv1.Server{Grpc: &bootstrapv1.Server_Grpc{Addr: ":0"}}
	srv, closer, err := GrpcServerProvider(first, second)(context.Background(), cfg)
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

	// 一次真实 unary 调用（标准健康服务），两个拦截器各触发一次。
	_ = grpcHealthStatus(t, srv.Endpoint())

	if got := snapshot(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("interceptor order = %v, want [first second]（登记顺序即链序）", got)
	}
}
