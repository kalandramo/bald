//go:build grpcgw

package bootstrap

import (
	"context"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"

	gatewaycontract "github.com/kalandramo/bald/transport/gateway/contract"

	"github.com/kalandramo/bald/pkg/appkit"

	baldv1 "github.com/kalandramo/bald/example/bald/api/gen"
)

// grpcgw 变体：API 角色让出 gin 面（WithHTTP(nil)），由 grpc-gateway 转码
// 服务 server.http 端口（接管该段的 Provider，声明在 std.Standard() 之后
// = 段归属末者胜）。契约 server.http.driver 留空即可。
func init() {
	variantOpts["grpcgw"] = []appkit.Option{
		appkit.OnRole(RoleAPI,
			appkit.WithHTTP(nil),
			gatewaycontract.HTTPProvider(registerGateway),
		),
	}
}

func registerGateway(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
	mux := runtime.NewServeMux()
	if err := baldv1.RegisterGreetServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	return mux, nil
}
