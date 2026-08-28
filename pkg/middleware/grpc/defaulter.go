package grpc

import (
	"context"

	"google.golang.org/grpc"
)

// RequestDefaulter 定义可被拦截器调用以填充请求默认值的接口。
type RequestDefaulter interface {
	Default()
}

// DefaulterInterceptor 是 gRPC 拦截器，在 handler 之前对请求执行默认值填充。
func DefaulterInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, rq any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if defaulter, ok := rq.(RequestDefaulter); ok {
			defaulter.Default()
		}
		return handler(ctx, rq)
	}
}
