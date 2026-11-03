package grpc

import (
	"context"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/berrors/grpcerr"
)

// ErrorInterceptor 把业务错误转成 gRPC status，保证错误语义跨服务完整透传。
//
// 为什么需要它：gRPC 的 handler 直接返回 error 时，框架内部会用
// status.Convert(err) 兜底，*berrors.Error 会被当成普通 error，
// 结果只有 Error() 的文本被塞进 Unknown，Code/Reason/Details 全丢。
// 经 grpcerr.ToStatus 转换后，接收端可用 grpcerr.FromStatus 还原完整语义。
//
// 转换规则（详见 bald/berrors/grpcerr）：
//   - 命中 *berrors.Error：用其 Code（→ gRPC codes）、Message、Reason、Details 构造，
//     并附 errdetails.ErrorInfo；
//   - 未命中（原生 error 或已是 gRPC status）：原样透传，语义不丢。
//
// 用法：**必须放在拦截器链最外层**（最先注册），这样内层（校验、认证等）
// 抛出的错误都能被它收口转换：
//
//	grpc.ChainUnaryInterceptor(
//	    grpcmw.ErrorInterceptor(),     // ← 最外层，收口所有错误
//	    grpcmw.RequestIDInterceptor(),
//	    grpcmw.ValidatorInterceptor(v),
//	    ...
//	)
//
// 注意：若把它放在内层，外层拦截器产生的错误将不会被转换。
func ErrorInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, rq any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, rq)
		if err == nil {
			return resp, nil
		}
		// ToStatus 已处理 nil 与非 *berrors.Error 的情况，这里无需再判断。
		return resp, grpcerr.ToStatus(err).Err()
	}
}

// ErrorStreamInterceptor 是 [ErrorInterceptor] 的 stream 面对偶，转换规则完全相同。
//
// 为什么 stream 面也需要它：grpc-go 在流结束时对返回的 error 做
// `status.Convert(err)`，而 *berrors.Error **没有**实现 `GRPCStatus()`，
// 于是 Code/Reason/Details 全丢、只剩文本落进 `Unknown`。没有这一层，
// [RateLimitStreamInterceptor] 的拒绝就到客户端时已经不是
// `RESOURCE_EXHAUSTED` 了——即「加了限流却看不出是被限流」。
//
// 链位同 unary：必须最外层。本包的 stream 拦截器目前只有收口与限流两个
// （认证/授权/审计至今只有 unary 形态）。
func ErrorStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := handler(srv, ss); err != nil {
			return grpcerr.ToStatus(err).Err()
		}
		return nil
	}
}
