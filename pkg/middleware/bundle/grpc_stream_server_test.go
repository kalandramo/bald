package bundle

// 本文件把 §6.2 未修项②端到端验一次：bundle.GRPCChain() 交出的
// ChainStreamInterceptor 必须真的落到 grpc.NewServer 上，并在**真实流式 RPC**
// 上表现为客户端可读的 RESOURCE_EXHAUSTED。
//
// 为什么用 grpc.health.v1：它是 grpc-go 自带的服务，`Health/Watch` 正是
// server-streaming 方法——省掉为本条断言写一份 .proto 并生成代码的代价，
// 又不必用假的 ServerStream 自说自话。

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// serveBundleGRPC 起一个真 gRPC server（127.0.0.1:0），选项全部来自 bundle，
// 并返回一个已连上它的客户端连接。
func serveBundleGRPC(t *testing.T, b *Bundle) *grpc.ClientConn {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer(b.GRPCChain()...)
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(srv, hs)
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("127.0.0.1:"+strconv.Itoa(lis.Addr().(*net.TCPAddr).Port),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestGRPCChain_StreamRateLimitEndToEnd 真流式调用上的限流：burst=1 时第一条
// Watch 建立成功，第二条以 RESOURCE_EXHAUSTED 被拒且 handler 从未执行。
// 这条断的是「选项有没有落到 grpc.NewServer」——unit 面怎么拼链都证明不了这点。
func TestGRPCChain_StreamRateLimitEndToEnd(t *testing.T) {
	rl, _, err := RateLimitFromMiddleware(mwWithRateLimit(0.0001, 1, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	conn := serveBundleGRPC(t, New(Public(), RateLimit(rl, false)))
	client := grpc_health_v1.NewHealthClient(conn)

	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	stream, err := client.Watch(firstCtx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("首条流建立应成功: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		// Recv 首帧失败可能是流已被对端结束；code 才是本条要断的。
		if status.Code(err) != codes.OK {
			t.Fatalf("首条流不该被限流: code=%v err=%v", status.Code(err), err)
		}
	}
	firstCancel()

	// 配额已被第一条流吃掉：第二条必须被拒。
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer secondCancel()
	second, err := client.Watch(secondCtx, &grpc_health_v1.HealthCheckRequest{})
	if err == nil {
		// 建立期不报错的话，拒绝一定在 Recv 上现形（服务端在 handler 前就返回错误）。
		_, err = second.Recv()
	}
	if err == nil {
		t.Fatal("期望第二条流被限流拒绝，实际成功")
	}
	if code := status.Code(err); code != codes.ResourceExhausted {
		t.Fatalf("客户端看到的 code = %v, want ResourceExhausted（若成了 Unknown，说明流上的 Error 收口层没在最外层）(err=%v)", code, err)
	}
}

// TestGRPCChain_UnaryRateLimitEndToEnd 对照：同一份选项在 unary 面上同样生效，
// 且与流面共用同一个桶（这条把 unary 的 Check 打进已耗尽的桶）。
func TestGRPCChain_UnaryRateLimitEndToEnd(t *testing.T) {
	rl, _, err := RateLimitFromMiddleware(mwWithRateLimit(0.0001, 1, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	conn := serveBundleGRPC(t, New(Public(), RateLimit(rl, false)))
	client := grpc_health_v1.NewHealthClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.Check(ctx, &grpc_health_v1.HealthCheckRequest{}); err != nil {
		t.Fatalf("首个 unary 应放行: %v", err)
	}

	_, err = client.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if code := status.Code(err); code != codes.ResourceExhausted {
		t.Fatalf("code = %v, want ResourceExhausted (err=%v)", code, err)
	}

	// 桶已被 unary 用光 → 流也建不起来，这是「两条链共用一个桶」的端到端形态。
	watchCtx, watchCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer watchCancel()
	stream, serr := client.Watch(watchCtx, &grpc_health_v1.HealthCheckRequest{})
	if serr == nil {
		_, serr = stream.Recv()
	}
	if status.Code(serr) != codes.ResourceExhausted {
		t.Fatalf("流面 code = %v, want ResourceExhausted（限流只挡了 unary）(err=%v)", status.Code(serr), serr)
	}
}
