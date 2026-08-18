package server

import (
	"context"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	baldoptions "github.com/kalandramo/bald/pkg/options"
)

// GRPCServer 封装 google.golang.org/grpc，实现 Server 契约。
// 默认注册 health check 与 reflection，方便运维探测与调试。
// 嵌入 *grpc.Server 提升其方法（如 RegisterService），内部统一经嵌入字段操作。
type GRPCServer struct {
	*grpc.Server
	opts *baldoptions.GRPCOptions
	addr string
	ln   net.Listener // 实际监听器，用于解析 Endpoint
}

// NewGRPCServer 基于已构建的 *grpc.Server 构造一个 GRPCServer。
func NewGRPCServer(opts *baldoptions.GRPCOptions, srv *grpc.Server) *GRPCServer {
	return &GRPCServer{
		Server: srv,
		opts:   opts,
		addr:   opts.Addr,
	}
}

// NewGRPCServerWithRegister 构造 gRPC 服务器并注册一个业务实现。
// register 回调用于把具体 Service 实现绑定到 gRPC Server。
func NewGRPCServerWithRegister(
	opts *baldoptions.GRPCOptions,
	unary []grpc.ServerOption,
	register func(s *grpc.Server),
) *GRPCServer {
	s := grpc.NewServer(unary...)
	healthpb.RegisterHealthServer(s, health.NewServer())
	reflection.Register(s)
	if register != nil {
		register(s)
	}
	return &GRPCServer{Server: s, opts: opts, addr: opts.Addr}
}

// Start 启动 gRPC 服务器（阻塞）。
func (s *GRPCServer) Start(ctx context.Context) error {
	lis, err := listen(s.addr)
	if err != nil {
		return err
	}
	s.ln = lis
	return s.Server.Serve(lis)
}

// Stop 优雅停止 gRPC 服务器（GracefulStop）。
func (s *GRPCServer) Stop(ctx context.Context) error {
	stopped := make(chan struct{})
	go func() {
		s.Server.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		s.Server.Stop()
		return ctx.Err()
	}
}

// Endpoint 返回实际监听地址（支持 ":0" 动态端口）。
func (s *GRPCServer) Endpoint() string {
	if s.ln != nil {
		return "grpc://" + s.ln.Addr().String()
	}
	return "grpc://" + s.addr
}
