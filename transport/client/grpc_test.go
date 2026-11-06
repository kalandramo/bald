package client

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/kalandramo/bald/registry"
	"github.com/kalandramo/bald/registry/inmemory"
)

// 测试服务用 ServiceDesc 直接注册（不生成桩）：覆盖单次调用与服务器流两种
// 形态即可验证超时拦截器与 resolver 接线。

const testServiceName = "client.test.Echo"

type echoService interface {
	Echo(context.Context, *emptypb.Empty) (*emptypb.Empty, error)
}

func echoHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(echoService).Echo(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/" + testServiceName + "/Echo"}
	return interceptor(ctx, in, info, func(ctx context.Context, req any) (any, error) {
		return srv.(echoService).Echo(ctx, req.(*emptypb.Empty))
	})
}

func watchHandler(_ any, stream grpc.ServerStream) error {
	select {
	case <-time.After(2 * time.Second):
		return nil
	case <-stream.Context().Done():
		return stream.Context().Err()
	}
}

func serviceDesc(impl echoService) *grpc.ServiceDesc {
	return &grpc.ServiceDesc{
		ServiceName: testServiceName,
		HandlerType: (*echoService)(nil),
		Methods:     []grpc.MethodDesc{{MethodName: "Echo", Handler: echoHandler}},
		Streams: []grpc.StreamDesc{{
			StreamName:    "Watch",
			Handler:       watchHandler,
			ServerStreams: true,
		}},
		Metadata: "client/test.proto",
	}
}

// startTestServer 起一个真实监听的测试服务，返回地址。
func startTestServer(t *testing.T, impl echoService) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	srv.RegisterService(serviceDesc(impl), impl)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

type fastEcho struct{}

func (fastEcho) Echo(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

// slowEcho 一直等到调用方截止期到期——用返回值把「超时真的传到了服务端」
// 变成可断言的错误码，而不是测时长。
type slowEcho struct{}

func (slowEcho) Echo(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	select {
	case <-time.After(2 * time.Second):
		return &emptypb.Empty{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestNewGRPCRejectsEmptyTarget(t *testing.T) {
	if _, err := NewGRPC(""); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("NewGRPC(\"\") err = %v, want empty-target error", err)
	}
}

func TestNewGRPCRejectsDiscoveryTargetWithoutSource(t *testing.T) {
	_, err := NewGRPC("discovery:///echo")
	if err == nil || !strings.Contains(err.Error(), "no discovery") {
		t.Fatalf("NewGRPC(discovery target) err = %v, want no-discovery error", err)
	}
}

func TestNewGRPCPlainTargetConstructs(t *testing.T) {
	conn, err := NewGRPC("127.0.0.1:1")
	if err != nil {
		t.Fatalf("NewGRPC: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
}

// 默认超时在无截止期的 ctx 上生效：服务端在截止期到期时返回，客户端拿到
// DeadlineExceeded（不是自己猜的时长断言）。
func TestGRPCUnaryCallTimeout(t *testing.T) {
	addr := startTestServer(t, slowEcho{})
	conn, err := NewGRPC(addr, WithCallTimeout(80*time.Millisecond))
	if err != nil {
		t.Fatalf("NewGRPC: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	start := time.Now()
	err = conn.Invoke(context.Background(), "/"+testServiceName+"/Echo", &emptypb.Empty{}, &emptypb.Empty{})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("Invoke err = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("call took %v; the 80ms default timeout did not apply", elapsed)
	}
}

// 调用方自带截止期时不被默认值覆盖：ctx 的 40ms 比默认 2s 更短，调用应按
// ctx 到期结束。
func TestGRPCUnaryCallTimeoutKeepsExistingDeadline(t *testing.T) {
	addr := startTestServer(t, slowEcho{})
	conn, err := NewGRPC(addr, WithCallTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("NewGRPC: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = conn.Invoke(ctx, "/"+testServiceName+"/Echo", &emptypb.Empty{}, &emptypb.Empty{})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("Invoke err = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("call took %v; caller deadline was overridden by the 2s default", elapsed)
	}
}

// 服务器流的默认超时覆盖整条流：Recv 在截止期到期时返回 DeadlineExceeded。
func TestGRPCStreamCallTimeout(t *testing.T) {
	addr := startTestServer(t, fastEcho{})
	conn, err := NewGRPC(addr, WithCallTimeout(80*time.Millisecond))
	if err != nil {
		t.Fatalf("NewGRPC: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	stream, err := conn.NewStream(context.Background(),
		&grpc.StreamDesc{ServerStreams: true}, "/"+testServiceName+"/Watch")
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	start := time.Now()
	err = stream.RecvMsg(&emptypb.Empty{})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("RecvMsg err = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("stream took %v; the 80ms default timeout did not apply", elapsed)
	}
}

// discovery:/// 目标经注册中心解析到真实后端：实例端点带 "grpc://" 前缀
// （bald 服务的注册形状），拨号前要被剥掉。
func TestGRPCDiscoveryResolvesRegisteredInstance(t *testing.T) {
	addr := startTestServer(t, fastEcho{})
	reg := inmemory.New()
	if err := reg.Register(context.Background(), &registry.ServiceInstance{
		ID:        "echo-1",
		Name:      "echo",
		Endpoints: []string{"grpc://" + addr},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	conn, err := NewGRPC("discovery:///echo", WithDiscovery(reg))
	if err != nil {
		t.Fatalf("NewGRPC: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Invoke(ctx, "/"+testServiceName+"/Echo", &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
		t.Fatalf("Invoke via discovery: %v", err)
	}
}
