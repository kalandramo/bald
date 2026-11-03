package bundle

// 本文件锁 §6.2 未修项①收口后的契约面：从**配置文本**（protojson，字段名与
// YAML 契约一致）出发，经 FromGRPCMiddleware 桥 → bundle → 两条链，断言
// 「写 server.grpc.middleware 段就能给 gRPC 限流/超时」这条用户可见的命题，
// 并钉住它与 HTTP 段的覆盖关系（更具体的段管更具体的面，没有静默盖写）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

func mustGRPCMiddleware(t *testing.T, jsonCfg string) *bootstrapv1.Server_Grpc_Middleware {
	t.Helper()
	mw := &bootstrapv1.Server_Grpc_Middleware{}
	if err := protojson.Unmarshal([]byte(jsonCfg), mw); err != nil {
		t.Fatalf("解析配置失败: %v", err)
	}
	return mw
}

// bridgeNew 把两份契约段翻译后的 Option 合成一个 Bundle，并在测试结束时调两侧桥
// 返回的 close（限流器释放）。
func bridgeNew(t *testing.T, httpMw *bootstrapv1.Server_Http_Middleware, grpcMw *bootstrapv1.Server_Grpc_Middleware) *Bundle {
	t.Helper()

	opts, closeHTTP, err := FromMiddleware(httpMw)
	if err != nil {
		t.Fatalf("http 桥报错: %v", err)
	}
	t.Cleanup(func() { _ = closeHTTP() })

	gopts, closeGRPC, err := FromGRPCMiddleware(grpcMw)
	if err != nil {
		t.Fatalf("grpc 桥报错: %v", err)
	}
	t.Cleanup(func() { _ = closeGRPC() })

	opts = append(opts, gopts...)
	return New(append([]Option{Public()}, opts...)...)
}

// TestFromGRPCMiddleware_NilIsNoOp 无 gRPC middleware 段 → 零 Option、链不受影响。
// 这条是「现有部署升级后行为不变」的判据。
func TestFromGRPCMiddleware_NilIsNoOp(t *testing.T) {
	opts, closeFn, err := FromGRPCMiddleware(nil)
	if err != nil || len(opts) != 0 {
		t.Fatalf("nil 段应返回零 Option 且不报错: opts=%d err=%v", len(opts), err)
	}
	if err := closeFn(); err != nil {
		t.Fatalf("no-op close 不应报错: %v", err)
	}

	base := New(Public())
	b := bridgeNew(t, nil, &bootstrapv1.Server_Grpc_Middleware{})
	if got, want := len(b.GRPCInterceptors()), len(base.GRPCInterceptors()); got != want {
		t.Fatalf("空段不该往 gRPC 链加层：%d vs 基线 %d", got, want)
	}
	if got, want := len(b.GRPCChain()), len(base.GRPCChain()); got != want {
		t.Fatalf("空段不该新增 ServerOption：%d vs 基线 %d", got, want)
	}
}

// TestFromGRPCMiddleware_FailsFast 显式声明了段却给非法值必须启动期报错——
// 「配了不生效且不吭声」是这座桥最坏的失败形态。
func TestFromGRPCMiddleware_FailsFast(t *testing.T) {
	for _, cfg := range []string{
		`{"rateLimit":{"rate":0,"burst":1}}`,
		`{"rateLimit":{"rate":10,"burst":0}}`,
		`{"timeout":{"defaultTimeoutMs":0}}`,
		`{"timeout":{"defaultTimeoutMs":-5}}`,
	} {
		_, _, err := FromGRPCMiddleware(mustGRPCMiddleware(t, cfg))
		if err == nil {
			t.Errorf("非法配置应 fail-fast: %s", cfg)
		}
	}
}

// TestFromGRPCMiddleware_RateLimitAppliesToGRPCOnly gRPC 段限流：链位与 HTTP 侧
// 相同（授权之后），但 gin 面**不受影响**——本段只说 gRPC 的容量。
func TestFromGRPCMiddleware_RateLimitAppliesToGRPCOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	b := bridgeNew(t, nil, mustGRPCMiddleware(t, `{"rateLimit":{"rate":0.0001,"burst":1,"wait":false}}`))

	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	var hits int
	for i := 0; i < 3; i++ {
		_, err := chainInvoke(b.GRPCInterceptors(), info,
			func(context.Context, any) (any, error) { hits++; return "ok", nil },
			context.Background(), nil)
		if i == 0 && err != nil {
			t.Fatalf("首个 gRPC 调用应放行: %v", err)
		}
		if i > 0 && status.Code(err) != codes.ResourceExhausted {
			t.Fatalf("第 %d 个 gRPC 调用 code = %v, want ResourceExhausted", i+1, status.Code(err))
		}
	}
	if hits != 1 {
		t.Fatalf("被限流的调用不应触达 handler，hits=%d want 1", hits)
	}

	// 同一份限流器不该出现在 gin 链上：HTTP 面没配段就是零开销省略。
	if got, want := len(b.Gin()), len(New(Public()).Gin()); got != want {
		t.Fatalf("gRPC 段不应给 gin 链加层：%d vs 基线 %d", got, want)
	}
	// 但 gRPC 的两面都要有：stream 链同样吃这个段（否则「配了 gRPC 限流，流却
	// 随便建」是半个漏风的口子）。
	if got, want := len(b.GRPCStreamInterceptors()), 2; got != want {
		t.Fatalf("gRPC 段的限流必须同时覆盖 stream 面：len=%d want %d", got, want)
	}
	r := gin.New()
	r.Use(b.Gin()...)
	r.GET("/x", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("gin 面第 %d 次请求 = %d, want 200（gRPC 段不该管 HTTP）", i+1, w.Code)
		}
	}
}

// TestFromGRPCMiddleware_TimeoutAppliesToUnaryNotStream 契约注释就说了 gRPC 超时
// 只管 unary：unary handler 拿到约等于配置值的截止期，流上什么都不变。
func TestFromGRPCMiddleware_TimeoutAppliesToUnaryNotStream(t *testing.T) {
	b := bridgeNew(t, nil, mustGRPCMiddleware(t,
		`{"rateLimit":{"rate":0.0001,"burst":8,"wait":false},"timeout":{"defaultTimeoutMs":40}}`))

	var seen context.Context
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	if _, err := chainInvoke(b.GRPCInterceptors(), info,
		func(c context.Context, _ any) (any, error) { seen = c; return "ok", nil },
		context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	dl, ok := seen.Deadline()
	if !ok {
		t.Fatal("gRPC timeout 段应给 unary handler 挂截止期")
	}
	if remaining := time.Until(dl); remaining <= 0 || remaining > time.Second {
		t.Fatalf("剩余 %v，期望接近 40ms", remaining)
	}

	// 流面：限流层在，超时层不在（handler 看到的 ctx 没有 deadline）。
	var streamDeadline bool
	if err := streamChainInvoke(t, b.GRPCStreamInterceptors(), &fakeStream{ctx: context.Background()},
		func(s grpc.ServerStream) error {
			_, streamDeadline = s.Context().Deadline()
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if streamDeadline {
		t.Error("unary 超时不该外溢到流上——那会把它变成「流的最大存活时长」")
	}

	// 也不该外溢到 HTTP 面：gRPC 段翻译出来的是 GRPCTimeout，不是 Timeout。
	if got, want := len(b.Gin()), len(New(Public()).Gin()); got != want {
		t.Fatalf("gRPC 段的 timeout 不该给 gin 链加层：%d vs 基线 %d", got, want)
	}
}

// TestFromGRPCMiddleware_OverridesHTTPSegmentBucket 两个段都给：**更具体的段管更
// 具体的面**。gRPC 用自己的小桶（burst=1 就拒），HTTP 用自己的大桶（burst=5 还能
// 放行），谁也没被谁静默盖掉。
func TestFromGRPCMiddleware_OverridesHTTPSegmentBucket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	httpMw := mustMiddleware(t, `{"rateLimit":{"rate":0.0001,"burst":5,"wait":false}}`)
	grpcMw := mustGRPCMiddleware(t, `{"rateLimit":{"rate":0.0001,"burst":1,"wait":false}}`)
	b := bridgeNew(t, httpMw, grpcMw)

	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	if _, err := chainInvoke(b.GRPCInterceptors(), info,
		func(context.Context, any) (any, error) { return "ok", nil }, context.Background(), nil); err != nil {
		t.Fatalf("gRPC 首次应放行: %v", err)
	}
	if _, err := chainInvoke(b.GRPCInterceptors(), info,
		func(context.Context, any) (any, error) { return "ok", nil }, context.Background(), nil); err == nil {
		t.Fatal("gRPC 段 burst=1，第二次应被自己的桶拒——说明它仍在吃 HTTP 那个大桶")
	}

	r := gin.New()
	r.Use(b.Gin()...)
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		if w.Code != http.StatusNoContent {
			t.Fatalf("HTTP 段 burst=5，第 %d 次请求却被拒（code=%d）——两个桶必须互不相干", i+1, w.Code)
		}
	}
}

// TestFromMiddleware_HTTPSegmentStillFeedsGRPC 只给 HTTP 段时的既有回落行为：
// gRPC 两面共用 HTTP 那个 limiter（不配 gRPC 段也至少有个兜底桶）。
// 这条守住「新增 gRPC 段不会让存量配置丢掉 gRPC 保护」。
func TestFromMiddleware_HTTPSegmentStillFeedsGRPC(t *testing.T) {
	httpMw := mustMiddleware(t, `{"rateLimit":{"rate":0.0001,"burst":1,"wait":false}}`)
	b := bridgeNew(t, httpMw, nil)

	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	if _, err := chainInvoke(b.GRPCInterceptors(), info,
		func(context.Context, any) (any, error) { return "ok", nil }, context.Background(), nil); err != nil {
		t.Fatalf("gRPC 首次应放行（与 gin 同桶，配额还没被吃掉）: %v", err)
	}

	// 流面也吃同一个桶：这里必须已被拒（上面那次 unary 用掉了唯一的令牌）。
	err := streamChainInvoke(t, b.GRPCStreamInterceptors(), &fakeStream{ctx: context.Background()},
		func(grpc.ServerStream) error { return nil })
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("stream 面 code = %v, want ResourceExhausted（三面共桶）(err=%v)", status.Code(err), err)
	}
}

// TestGRPCTimeout_OptionBeatsHTTPTimeout HTTP 段的预算给 gin，gRPC 段的给 gRPC：
// 999ms vs 40ms 差得够远，断言 handler 看到的是后者。
func TestGRPCTimeout_OptionBeatsHTTPTimeout(t *testing.T) {
	httpMw := mustMiddleware(t, `{"timeout":{"defaultTimeoutMs":999}}`)
	grpcMw := mustGRPCMiddleware(t, `{"timeout":{"defaultTimeoutMs":40}}`)
	b := bridgeNew(t, httpMw, grpcMw)

	var seen context.Context
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	if _, err := chainInvoke(b.GRPCInterceptors(), info,
		func(c context.Context, _ any) (any, error) { seen = c; return "ok", nil },
		context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	dl, ok := seen.Deadline()
	if !ok {
		t.Fatal("gRPC handler 应有截止期")
	}
	if remaining := time.Until(dl); remaining > 500*time.Millisecond {
		t.Fatalf("剩余 %v——gRPC 段没盖过 HTTP 段（999ms）", remaining)
	}
}

// TestFromGRPCMiddleware_StreamRejectEndToEnd 端到端把用户那句「写 gRPC 配置就能
// 限流」跑通：配置文本 → 桥 → bundle.GRPCChain() → 真 grpc.NewServer → 真流式
// RPC 被拒且 code 可读。
func TestFromGRPCMiddleware_StreamRejectEndToEnd(t *testing.T) {
	b := bridgeNew(t, nil, mustGRPCMiddleware(t, `{"rateLimit":{"rate":0.0001,"burst":1,"wait":false}}`))

	conn := serveBundleGRPC(t, b)
	client := grpc_health_v1.NewHealthClient(conn)

	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	firstStream, err := client.Watch(firstCtx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("首条流应建立成功: %v", err)
	}
	// 必须读到首帧：客户端的 Watch 调用在本地建流时就返回，服务端拦截器（扣
	// 令牌的那次）要等流真正建立才跑。不等这一帧就是在赌调度，第二次的结果不确定。
	if _, err := firstStream.Recv(); err != nil {
		t.Fatalf("首条流应正常收到状态帧: %v", err)
	}
	firstCancel()

	secondCtx, secondCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer secondCancel()
	stream, err := client.Watch(secondCtx, &grpc_health_v1.HealthCheckRequest{})
	if err == nil {
		_, err = stream.Recv()
	}
	if code := status.Code(err); code != codes.ResourceExhausted {
		t.Fatalf("code = %v, want ResourceExhausted (err=%v)", code, err)
	}
}
