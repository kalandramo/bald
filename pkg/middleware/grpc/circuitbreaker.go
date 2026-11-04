package grpc

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/circuitbreaker"
)

// ReasonCircuitOpen 是开态拒绝的稳定业务标识（berrors.Is 按 Reason 匹配）。
// 与 [ReasonRateLimited] 刻意分开：两者对调用方是两种不同的答复——「你超配额」
// 与「我现在不接受任何请求」，重试策略也不同（前者要退避到下个窗口，后者要
// 换实例或等休眠窗）。
const ReasonCircuitOpen = "CIRCUIT_OPEN"

// failureCodes 是 gRPC 面上算「服务侧失败」的 status code 集合。
//
// 只列服务侧的错：客户端侧的（InvalidArgument / NotFound / PermissionDenied /
// FailedPrecondition / Aborted / OutOfRange / Unauthenticated）是调用方的错，
// 计入失败率会让一个坏客户端替全体用户开窗。
//
// ResourceExhausted 也**不在**这个集合里，尽管它字面像「服务满了」：那是
// 本服务限流层自己给出的答案，把它计入失败等于让熔断消费自己的拒流——配额
// 打满时熔断跟着开窗，两层从互补变成互相放大。
var failureCodes = map[codes.Code]bool{
	codes.Internal:         true,
	codes.Unknown:          true,
	codes.Unavailable:      true,
	codes.DeadlineExceeded: true,
	codes.DataLoss:         true,
}

// CircuitBreakerInterceptor 用 [circuitbreaker.CircuitBreaker] 保护 unary 调用。
//
// 与限流拦截器（[RateLimitInterceptor]）同处一条链、各判一件事：限流问「这个主体
// 还有没有配额」，熔断问「这个服务现在值不值得真做」。拒绝的 code 因此不同：
// 限流回 RESOURCE_EXHAUSTED，熔断回 UNAVAILABLE（客户端库按可重试处理，且
// 语义正是「服务暂时不可用」而不是「你太多」）。错误用 *berrors.Error 构造，
// 经链首 [ErrorInterceptor] 收口后 Code/Reason 可跨服务还原。
//
// 失败口径见 [failureCodes]。上报同样在 defer 里完成，panic 路径记失败后原样
// 抛出（外层 [RecoveryInterceptor] 仍会把它转成 Internal）：契约里「Allow 放行
// 即欠一次 Mark*」，而 hystrix 半开态的在途探针标记只有 Mark* 会复位——漏掉
// 一次上报不是少统计一条，是熔断从此永久拒绝。被本层拒掉的请求不欠上报：
// Allow 已返回错误，按契约不能再调 Mark*——否则「拒绝」会在喂养自己的失败率。
//
// 为什么没有 stream 对偶：一条流的「失败」没有一致答案——建流成功后流还能跑
// 十分钟，按建流算则开窗与真实退化无关，按流终态算则一条长流会长期占住半开
// 探针（其它流全被拒）。要这个语义请自己组 grpc.ChainStreamInterceptor。
func CircuitBreakerInterceptor(cb circuitbreaker.CircuitBreaker) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, rq any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		if cb == nil {
			return handler(ctx, rq)
		}
		if allowErr := cb.Allow(); allowErr != nil {
			return nil, berrors.Unavailable(ReasonCircuitOpen).
				WithMessage("grpc: circuit breaker open: %v", allowErr).WithCause(allowErr)
		}
		defer func() {
			if p := recover(); p != nil {
				cb.MarkFailure()
				panic(p)
			}
			if IsFailureStatus(err) {
				cb.MarkFailure()
				return
			}
			cb.MarkSuccess()
		}()
		return handler(ctx, rq)
	}
}

// IsFailureStatus 报告一个错误是否算「服务侧失败」（熔断的失败口径）。导出
// 是为了让业务在自组拦截器、或按 [circuitbreaker.CircuitBreaker] 自己配对上报时
// 用同一把尺子，而不是各处再写一份 code 集合。
//
// 分类顺序照抄 grpcerr.ToStatus：本层在链首 [ErrorInterceptor] **之内**，看到的
// 是收口前的原生错误，所以得自己走一遍同样的判定。
//  1. *berrors.Error → 用它的 Code（与 gRPC codes 数值 1:1，见 berrors/code.go）；
//  2. 已是 gRPC status → 用它的 code；
//  3. context 的两个哨兵 → Canceled 是客户端撤单（不算失败），DeadlineExceeded
//     是服务没在预算内回来（算失败）；
//  4. 其余原生错误 → 与 grpc-go 一样兜成 Unknown，算失败。
//
// 为什么不能只看 status.Code(err)：*berrors.Error **不**实现 GRPCStatus()（见
// error.go 注释），status.Convert 会把任何 berrors 一律兜成 Unknown，而 Unknown
// 正好在失败集合里。照那条路走，一次 authz 拒绝（PermissionDenied）或一次非法
// 参数都会被算成服务侧失败——正是本层要避免的「坏客户端替所有人开窗」。原生
// context.Canceled 同样会在判定之前被兜成 Unknown。
func IsFailureStatus(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := berrors.FromError(err); ok {
		return failureCodes[codes.Code(e.Code)]
	}
	if st, ok := status.FromError(err); ok {
		return failureCodes[st.Code()]
	}
	switch {
	case errors.Is(err, context.Canceled):
		return false
	case errors.Is(err, context.DeadlineExceeded):
		return true
	}
	return failureCodes[codes.Unknown]
}
