package grpc

import (
	"context"

	"google.golang.org/grpc"
)

// RequestValidator 定义自定义请求校验接口。
type RequestValidator interface {
	Validate(ctx context.Context, rq any) error
}

// ValidatorInterceptor 是 gRPC 拦截器，在 handler 之前对请求执行校验。
func ValidatorInterceptor(validator RequestValidator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, rq any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := validator.Validate(ctx, rq); err != nil {
			return nil, err
		}
		return handler(ctx, rq)
	}
}
