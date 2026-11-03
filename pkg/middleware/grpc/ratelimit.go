package grpc

import (
	"context"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/ratelimit"
)

// ReasonRateLimited 是本层所有拒绝的稳定业务标识（berrors.Is 按 Reason 匹配，
// 忽略 Code/Message）。快速失败与排队失败共用它：调用方要区分两者读 Message，
// 而不是把同一个「被限流」拆成两个键。
const ReasonRateLimited = "RATE_LIMITED"

// RateLimitInterceptor 用一个 [ratelimit.Limiter] 对 unary 调用限流。
//
// 与 gin 侧（pkg/middleware/gin/ratelimit.go）共用同一个 limiter 接口，差别只在
// 「拒绝怎么表达」：gin 回 429，这里回 RESOURCE_EXHAUSTED。错误用
// *berrors.Error 构造，经链首 ErrorInterceptor 收口后 Code/Reason 能跨服务透传
// （而不是被 status.Convert 兜成空 Unknown）。
//
// wait=false（快速失败，默认口径）：令牌不足立即拒绝，不进 handler，保护下游。
// wait=true（平滑）：阻塞到令牌可用或 ctx 取消；取消同样以限流错误结束——
// 客户端已经断开，继续排队没有意义。
//
// 链序：与 gin 一致，挂在认证/授权**之后**（见 bundle.GRPCInterceptors），
// 这样被拒的多是有配额的主体，匿名流量无法先耗尽配额。代价同样是不保护
// 认证层自身；需要在认证前限流的业务把本拦截器自行挪到链首。
//
// 未覆盖：[ratelimit.InflightLimiter] 的 `Done(rtt)` 配对上报（BBR 自适应需要）。
// gin 侧同样没做，两面口径保持一致；要用 BBR 的 RTT 反馈得在链外自行配对，
// 别指望这里。stream 面见 [RateLimitStreamInterceptor]。
func RateLimitInterceptor(l ratelimit.Limiter, wait bool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, rq any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if l == nil {
			return handler(ctx, rq)
		}
		if wait {
			if err := l.Wait(ctx); err != nil {
				return nil, berrors.ResourceExhausted(ReasonRateLimited).
					WithMessage("grpc: rate limit wait failed: %v", err).WithCause(err)
			}
			return handler(ctx, rq)
		}
		ok, err := l.Allow()
		if err != nil {
			return nil, berrors.ResourceExhausted(ReasonRateLimited).
				WithMessage("grpc: rate limiter unavailable: %v", err).WithCause(err)
		}
		if !ok {
			return nil, berrors.ResourceExhausted(ReasonRateLimited).WithMessage("grpc: rate limit exceeded")
		}
		return handler(ctx, rq)
	}
}

// RateLimitStreamInterceptor 是 [RateLimitInterceptor] 的 stream 面对偶，与它
// 共用同一个 limiter，所以一条流与一次 unary 调用扣的是同一份配额——不会出现
// 「HTTP 打满了，gRPC 流还能无限建」这种半边漏风的限流。
//
// 计费口径：**每条流建立时扣一个令牌，之后不再计**。这不是偷懒，是这一层的
// 语义边界——stream 拦截器只在流建立时被调用一次，handler 里的
// Send/Recv 循环在它之下自由运行。要按消息限流属于业务层的背压
// （或 [ratelimit.InflightLimiter] 的 inflight 口径），别指望这里：一条长活
// 流（订阅、watch、双向会话）此后占用多少消息都不再消耗令牌。后果要清楚：
// 洪水型「建了就断」的流会被这一层挡住，洪水型「一条流里狂发」的不会。
//
// wait=true 时排队用的是 ss.Context()，客户端断开即退出等待（与 unary 同）。
// 拒绝的错误形态与 unary 完全一致：RESOURCE_EXHAUSTED + [ReasonRateLimited]，
// 经链首 [ErrorStreamInterceptor] 收口后可跨服务还原。
func RateLimitStreamInterceptor(l ratelimit.Limiter, wait bool) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if l == nil {
			return handler(srv, ss)
		}
		if wait {
			if err := l.Wait(ss.Context()); err != nil {
				return berrors.ResourceExhausted(ReasonRateLimited).
					WithMessage("grpc: rate limit stream wait failed: %v", err).WithCause(err)
			}
			return handler(srv, ss)
		}
		ok, err := l.Allow()
		if err != nil {
			return berrors.ResourceExhausted(ReasonRateLimited).
				WithMessage("grpc: rate limiter unavailable: %v", err).WithCause(err)
		}
		if !ok {
			return berrors.ResourceExhausted(ReasonRateLimited).WithMessage("grpc: rate limit exceeded")
		}
		return handler(srv, ss)
	}
}
