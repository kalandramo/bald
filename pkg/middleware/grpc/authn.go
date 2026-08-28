package grpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/errors"
	"github.com/kalandramo/bald/pkg/log"
)

// TokenExtractor 从 gRPC metadata 中解析出用户 ID。具体 token 形态由调用方实现。
type TokenExtractor interface {
	Extract(ctx context.Context, md metadata.MD) (userID string, err error)
}

// UserRetriever 根据用户 ID 获取用户名，用于把身份注入 context。
type UserRetriever interface {
	GetUser(ctx context.Context, userID string) (username string, err error)
}

// AuthnInterceptor 是 gRPC 认证拦截器：解析 token 并将用户信息注入 context。
func AuthnInterceptor(extractor TokenExtractor, retriever UserRetriever) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)

		userID, err := extractor.Extract(ctx, md)
		if err != nil {
			e := errors.Unauthenticated("TOKEN_INVALID").WithMessage("%s", err.Error())
			log.GetLogger().Error(ctx, "failed to parse token", "error", err)
			return nil, e
		}

		username, err := retriever.GetUser(ctx, userID)
		if err != nil {
			e := errors.Unauthenticated("USER_NOT_FOUND").WithMessage("%s", err.Error())
			return nil, e
		}

		ctx = contextx.WithUserID(ctx, userID)
		ctx = contextx.WithUsername(ctx, username)
		return handler(ctx, req)
	}
}
