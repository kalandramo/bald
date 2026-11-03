package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
)

// TimeoutInterceptor 给**没有携带 deadline** 的调用挂上服务端默认超时。
//
// 为什么需要它：客户端不设 deadline 时，一条慢 RPC 会无限期占住它的 stream、
// goroutine 与后端资源。gin 链有 Timeout 段（pkg/middleware/gin/timeout.go），
// gRPC 链此前没有任何框架级兜底，两面是不对称的。
//
// 语义，以及它**不**保证什么（与 gin 侧的差异必须讲清，别当成同一个东西）：
//   - 已有 deadline 的调用原样放过。框架不缩短调用方给的预算（与 appkit
//     端点等待同口径）。「取 remaining 与 d 较小者」会把客户端刻意设的长预算
//     腰斩，那不是这一层该有的权力。
//   - 它约束的是 handler 可见的 ctx：handler 得监听 ctx.Done()/ctx.Err() 才会
//     真的停下。Go 不能杀 goroutine，grpc-go 也要等 handler 返回才写 status，
//     所以本层保证的是「handler 拿得到一个会到期的上下文」，**不是**「响应
//     按时返回」。
//   - 想让响应硬性按时返回，就得把 handler 丢进 goroutine 并提前 return。
//     那样该 goroutine 里的 panic 会绕过外层 RecoveryInterceptor（它只覆盖
//     自己调用栈上的 handler），进程直接崩。这个代价不值得，本层不做。
//
// d <= 0 时不挂超时（等同不装配）：把 0 当「立即到期」会让所有 RPC 失败，
// 而 0 更常见的原因是配置段留空。
func TimeoutInterceptor(d time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, rq any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if d <= 0 {
			return handler(ctx, rq)
		}
		if _, ok := ctx.Deadline(); ok {
			return handler(ctx, rq)
		}
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return handler(ctx, rq)
	}
}

// TimeoutStreamInterceptor 给 stream 调用挂服务端默认超时，规则与
// [TimeoutInterceptor] 逐条相同（不腰斩调用方的 deadline、d<=0 等同不装配、
// 到期只体现在 handler 可见的 ctx 上、不把 handler 丢进 goroutine 提前返回）。
// 区别在于 ctx 要经 [ServerStreamWithContext] 换进流本身，否则 handler 里
// `ss.Context()` 拿到的还是原来那个没有截止期的上下文。
//
// **它不在 bundle 的默认 stream 链上，这是有意的**：stream handler 的生命周期
// 就是整条流，所以「一个 d」在 unary 上是请求超时，在流上会静默变成
// 「流的最大存活时长」——一条订阅/watch/双向会话到点就被砍。HTTP 侧那段
// `server.http.middleware.timeout` 的语义是「单次请求」，把它原样套到流上
// 等于替业务做一个它没做过的决定，而且长流被砍不报错、只表现为「流莫名结束」。
// 要这个语义就显式挂它（自己组 grpc.ChainStreamInterceptor）：
//
//	grpc.ChainStreamInterceptor(grpcmw.TimeoutStreamInterceptor(30*time.Second))
//
// 真想要「按活动计而不按总时长计」的空闲超时（idle），那是另一种旋钮
// （每次 Recv/Send 成功就重置截止期），不在本层；放在这里会让「timeout」
// 一个字段同时表达两种完全不同的预算，业务无法预期。
func TimeoutStreamInterceptor(d time.Duration) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if d <= 0 {
			return handler(srv, ss)
		}
		ctx := ss.Context()
		if _, ok := ctx.Deadline(); ok {
			return handler(srv, ss)
		}
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return handler(srv, ServerStreamWithContext(ss, ctx))
	}
}

// ServerStreamWithContext 返回一个只替换 Context() 的 grpc.ServerStream 视图。
// 其余方法（SetHeader/SendMsg/RecvMsg…）全部内嵌委托给 ss 本身，因此包装
// 不改变流的收发行为——只有取上下文的动作换了来源。
func ServerStreamWithContext(ss grpc.ServerStream, ctx context.Context) grpc.ServerStream {
	return &streamContext{ServerStream: ss, ctx: ctx}
}

type streamContext struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *streamContext) Context() context.Context { return s.ctx }
