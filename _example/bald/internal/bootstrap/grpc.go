package bootstrap

import (
	"google.golang.org/grpc"

	greetv1 "github.com/kalandramo/bald/example/bald/greetv1"
	grpcmw "github.com/kalandramo/bald/pkg/middleware/grpc"
)

// newGRPCChain 构造 gRPC 拦截器链（由 customOptions 的 WithGRPCOptions 在
// Run 期求值）。ErrorInterceptor 必须最外层，否则错误码在 gRPC 边界丢失。
func newGRPCChain() []grpc.ServerOption {
	return []grpc.ServerOption{grpc.ChainUnaryInterceptor(
		grpcmw.ErrorInterceptor(),
		grpcmw.RequestIDInterceptor(),
		grpcmw.UnaryObservability(),
		grpcmw.DefaulterInterceptor(),
		grpcmw.ValidatorInterceptor(greetv1.Validator),
	)}
}
