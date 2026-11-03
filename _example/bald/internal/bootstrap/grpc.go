package bootstrap

import (
	"google.golang.org/grpc"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/middleware/bundle"
	grpcmw "github.com/kalandramo/bald/pkg/middleware/grpc"

	greetv1 "github.com/kalandramo/bald/example/bald/greetv1"
)

// newGRPCChain 构造 gRPC 拦截器链（由 customOptions 的
// WithGRPCOptionsContract 在契约装载后求值一次）。
//
// 横切层（Error/Recovery/RequestID/观测/限流/超时）由 bundle 固化链序并读取契约
// server.grpc.middleware 段——配置里写的 rate_limit 真的返回 RESOURCE_EXHAUSTED，
// timeout 真的给 handler 一个会到期的 ctx。业务自有的 Defaulter/Validator 挂在
// bundle 链内侧（靠近 handler），这是唯一留在应用手里的链序决定。
//
// 认证姿态与 HTTP 面对称：契约里没有 authn 段，「本服务公开」由下面这一行显式
// 宣布（bundle.Public()），接入认证时改为 bundle.Authn(<authenticator>)。
func newGRPCChain(mw *bootstrapv1.Server_Grpc_Middleware) ([]grpc.ServerOption, func() error, error) {
	opts, closeChain, err := bundle.FromGRPCMiddleware(mw)
	if err != nil {
		return nil, nil, err
	}
	chain := bundle.New(append([]bundle.Option{
		bundle.Public(),
	}, opts...)...).GRPCChain()
	return append(chain, grpc.ChainUnaryInterceptor(
		grpcmw.DefaulterInterceptor(),
		grpcmw.ValidatorInterceptor(greetv1.Validator),
	)), closeChain, nil
}
