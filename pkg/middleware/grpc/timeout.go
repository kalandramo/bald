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
