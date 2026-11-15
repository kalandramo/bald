package resiliency

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/kalandramo/bald/circuitbreaker/sentinel"

	sentinelapi "github.com/alibaba/sentinel-golang/api"
	scb "github.com/alibaba/sentinel-golang/core/circuitbreaker"
)

// 真实 gRPC 服务 + 真实 sentinel 熔断器：证明 GRPCDialOption 真的挂进 Dial 链，
// 且跳闸后快速失败（不打服务端）、睡窗后半开探针经成功调用愈合。服务用
// ServiceDesc 直接注册（不生成桩），与 transport/client 测试同款。

const integrationService = "resiliency.test.Echo"

// resSeq 派生唯一资源名：sentinel 规则表进程级共享，每个实例独占一个资源。
var resSeq atomic.Uint32

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

func TestGRPCDialOptionSentinelTripAndRecover(t *testing.T) {
	if err := sentinelapi.InitDefault(); err != nil {
		t.Fatalf("sentinelapi.InitDefault: %v", err)
	}
	resource := fmt.Sprintf("resiliency.test.cb.%d", resSeq.Add(1))
	if _, err := scb.LoadRules([]*scb.Rule{{
		Resource:         resource,
		Strategy:         scb.ErrorCount,
		Threshold:        2,
		MinRequestAmount: 2,
		RetryTimeoutMs:   50,
		StatIntervalMs:   10000,
	}}); err != nil {
		t.Fatalf("LoadRules: %v", err)
	}

	impl := &echoImpl{}
	impl.failing.Store(true)
	addr := startEcho(t, impl)

	breaker := sentinel.New(resource)
	defer func() { _ = breaker.Close() }()

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

	// 下游愈合 + 睡窗过期：半开探针经成功调用回到 Closed。RetryTimeoutMs=50ms，
	// 留 100ms 余量再轮询，避免 CI 调度抖动判红。
	impl.failing.Store(false)
	time.Sleep(100 * time.Millisecond)
	if err := call(); err != nil {
		t.Fatalf("half-open probe after heal: %v", err)
	}
	if breaker.State() != circuitbreaker.StateClosed {
		t.Fatalf("state=%v, want Closed after successful probe", breaker.State())
	}
}