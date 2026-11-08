package resiliency

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/kalandramo/bald/circuitbreaker"
	"github.com/kalandramo/bald/circuitbreaker/hystrix"
)

// 真实 gRPC 服务 + 真实 hystrix：证明 GRPCDialOption 真的挂进 Dial 链，且
// 跳闸后快速失败（不打服务端）、睡窗后半开探针经成功调用愈合。服务用
// ServiceDesc 直接注册（不生成桩），与 transport/client 测试同款。

const integrationService = "resiliency.test.Echo"

type echoSvc interface {
	Echo(context.Context, *emptypb.Empty) (*emptypb.Empty, error)
}

type echoImpl struct{ failing atomic.Bool }

func (e *echoImpl) Echo(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	if e.failing.Load() {
		return nil, status.Error(codes.Unavailable, "downstream down")
	}
	return &emptypb.Empty{}, nil
}

func echoHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	return srv.(echoSvc).Echo(ctx, in)
}

func startEcho(t *testing.T, impl echoSvc) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: integrationService,
		HandlerType: (*echoSvc)(nil),
		Methods:     []grpc.MethodDesc{{MethodName: "Echo", Handler: echoHandler}},
		Metadata:    "resiliency/test.proto",
	}, impl)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func TestGRPCDialOptionHystrixTripAndRecover(t *testing.T) {
	impl := &echoImpl{}
	impl.failing.Store(true)
	addr := startEcho(t, impl)

	breaker := hystrix.New(
		hystrix.WithErrorThreshold(0.5),
		hystrix.WithRequestVolumeThreshold(2),
		hystrix.WithSleepWindow(30*time.Millisecond),
		hystrix.WithWindow(time.Second),
	)
	defer breaker.Close()

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		GRPCDialOption(Policy{Breaker: breaker}),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	call := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return conn.Invoke(ctx, "/"+integrationService+"/Echo", &emptypb.Empty{}, &emptypb.Empty{})
	}

	// 喂失败直到跳闸：跳闸前每次都是 Unavailable，跳闸那次起返回 ErrCircuitOpen。
	tripped := false
	for i := 0; i < 10; i++ {
		err := call()
		if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
			tripped = true
			break
		}
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("pre-trip call %d: err=%v, want Unavailable", i, err)
		}
	}
	if !tripped {
		t.Fatal("breaker never tripped after repeated Unavailable")
	}
	if breaker.State() != circuitbreaker.StateOpen {
		t.Fatalf("state=%v, want Open", breaker.State())
	}

	// 开态快速失败：再调仍 ErrCircuitOpen（服务端不被打）。
	if err := call(); !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("open-state call err=%v, want ErrCircuitOpen", err)
	}

	// 下游愈合 + 睡窗过期：半开探针经成功调用回到 Closed。
	impl.failing.Store(false)
	time.Sleep(40 * time.Millisecond)
	if err := call(); err != nil {
		t.Fatalf("half-open probe after heal: %v", err)
	}
	if breaker.State() != circuitbreaker.StateClosed {
		t.Fatalf("state=%v, want Closed after successful probe", breaker.State())
	}
}