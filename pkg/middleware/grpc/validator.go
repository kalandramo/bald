package grpc

import (
	"context"

	"google.golang.org/grpc"
)

// MessageValidator 校验一个请求对象，返回 error 表示校验不通过。
//
// 本包刻意**不绑定任何具体校验库**（与 P5 依赖治理原则一致：核心只依赖接口/函数类型，
// 具体实现由调用方注入）。因此：
//   - 声明式字段规则（proto 的 buf.validate 注解）用 protovalidate；
//   - 复杂命令式逻辑（查库、权限比对）用 pkg/validation 的分发器或手写函数；
//   - 两者可串联：先跑注解规则，再跑自定义逻辑。
//
// 常见的注入形态：
//
//	// 1. protovalidate（需先 go get buf.build/go/protovalidate）
//	grpcmw.ValidatorInterceptor(func(ctx context.Context, rq any) error {
//	    msg, ok := rq.(proto.Message)
//	    if !ok {
//	        return nil
//	    }
//	    return protovalidate.Validate(msg) // 错误再经 berrors.BadRequest 映射
//	})
//
//	// 2. pkg/validation 分发器（框架自带，零外部依赖）
//	v, err := validation.NewValidator(myValidator{})
//	grpcmw.ValidatorInterceptor(v.Validate)
//
//	// 3. 两者串联
//	grpcmw.ValidatorInterceptor(func(ctx context.Context, rq any) error {
//	    if msg, ok := rq.(proto.Message); ok {
//	        if err := protovalidate.Validate(msg); err != nil {
//	            return err
//	        }
//	    }
//	    return v.Validate(ctx, rq) // 复杂逻辑
//	})
type MessageValidator func(ctx context.Context, rq any) error

// ValidatorInterceptor 是 gRPC 拦截器，在 handler 之前对请求执行校验。
//
// validate 为 nil 时拦截器退化为空操作（不做校验）；这与旧实现「传 nil 校验器
// 导致全部请求静默放行」不同——现在的语义是显式、可见的：要么传回调，要么不挂拦截器。
func ValidatorInterceptor(validate MessageValidator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, rq any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if validate != nil {
			if err := validate(ctx, rq); err != nil {
				return nil, err
			}
		}
		return handler(ctx, rq)
	}
}
