package server

import (
	"context"
	"net/http"
	"sync"

	baldoptions "github.com/kalandramo/bald/pkg/options"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// GatewayServer 是 grpc-gateway 反向代理：将 REST 请求转发到本地 gRPC 服务。
// 复用 HTTPServer 承载 mux，实现 Server 契约。
type GatewayServer struct {
	*HTTPServer
	conn      *grpc.ClientConn // 到后端 gRPC 的连接，Stop 时关闭
	closeOnce sync.Once
}

// NewGatewayServer 构造网关服务器。
//   - httpOpts: 网关监听地址
//   - grpcAddr: 被代理的 gRPC 服务地址（如 ":9090"）
//   - register: 将 grpc-gateway 的 HTTP handler 注册到 mux 的回调
func NewGatewayServer(
	httpOpts *baldoptions.HTTPOptions,
	grpcAddr string,
	register func(ctx context.Context, mux *http.ServeMux, conn *grpc.ClientConn) error,
) (*GatewayServer, error) {
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	if register != nil {
		if err := register(context.Background(), mux, conn); err != nil {
			return nil, err
		}
	}
	return &GatewayServer{
		HTTPServer: NewHTTPServer(httpOpts, mux),
		conn:       conn,
	}, nil
}

// Stop 停止网关时同时关闭到后端 gRPC 的连接。
func (s *GatewayServer) Stop(ctx context.Context) error {
	defer s.closeOnce.Do(func() { _ = s.conn.Close() })
	return s.HTTPServer.Stop(ctx)
}
