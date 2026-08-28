package grpc

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/kalandramo/bald/pkg/contextx"
)

// RequestIDInterceptor 是 gRPC 拦截器，用于设置请求 ID：
// 优先从请求 metadata 的 X-Request-ID 获取，缺失时生成新的 UUID；将请求 ID
// 写入 incoming metadata（随响应回写客户端）并记录到 context。
func RequestIDInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		var requestID string
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if ids := md["x-request-id"]; len(ids) > 0 {
				requestID = ids[0]
			}
		}

		if requestID == "" {
			requestID = uuid.New().String()
			md, _ := metadata.FromIncomingContext(ctx)
			md = md.Copy()
			md.Append("x-request-id", requestID)
			ctx = metadata.NewIncomingContext(ctx, md)
		}

		// 将请求 ID 回写到响应 Header Metadata。
		_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", requestID))

		ctx = contextx.WithRequestID(ctx, requestID)
		return handler(ctx, req)
	}
}
