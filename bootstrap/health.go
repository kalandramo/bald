package bootstrap

import (
	"context"
	"sync"
	"time"

	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/kalandramo/bald/health"
	"github.com/kalandramo/bald/transport"
	grpcserver "github.com/kalandramo/bald/transport/grpc"
)

// defaultHealthInterval 是就绪状态同步到 gRPC 标准健康服务的默认间隔。
const defaultHealthInterval = 2 * time.Second

// grpcHealthServer 在 [grpcserver.GRPCServer] 之上叠加「就绪状态推送」：
// 后台轮询 [health.Health] 并把结果 SetServingStatus 给 gRPC 标准健康服务
// （"" = 整体服务），使 K8s grpc 探针与 HTTP /readyz 同源。
//
// 分层理由：gRPC health 是拉模型（探针主动 Check），必须由服务端推状态；
// 但"判断业务就绪"不属于协议实现。注册标准健康服务留在 transport/grpc，
// 推送留在装配层（见《Bald 健康检查装配设计》）。
type grpcHealthServer struct {
	*grpcserver.GRPCServer

	health   *health.Health
	interval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
}

// NewGRPCHealthServer 在 gRPC server 之上叠加「就绪状态推送」，返回可直接交给
// appkit.Servers 的 transport.Server：后台轮询 [health.Health] 并把结果
// SetServingStatus 给 gRPC 标准健康服务（"" = 整体服务），使 K8s grpc 探针与
// HTTP /readyz 同源。interval <= 0 时用默认间隔（2s）。
//
// 手工装配（如 CLI 生成的 app、不用 FromBootstrap 的装配代码）用它；走
// bootstrap 装配的路径由 [GrpcServerProvider] 的 WithGRPCHealth 自动完成。
func NewGRPCHealthServer(srv *grpcserver.GRPCServer, h *health.Health, interval time.Duration) transport.Server {
	if interval <= 0 {
		interval = defaultHealthInterval
	}
	return &grpcHealthServer{GRPCServer: srv, health: h, interval: interval}
}

// Start 起轮询后阻塞启动 gRPC 服务；Serve 返回（含 listen 失败）即取消轮询，
// 不残留 goroutine。
func (s *grpcHealthServer) Start(ctx context.Context) error {
	rctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	if hs := s.HealthServer(); hs != nil {
		go s.poll(rctx, hs)
	}

	err := s.GRPCServer.Start(ctx)
	cancel()
	return err
}

// Stop 先取消轮询再优雅停止 gRPC 服务。
func (s *grpcHealthServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return s.GRPCServer.Stop(ctx)
}

// poll 立即同步一次（尽快暴露未就绪），之后按 interval 周期同步。
func (s *grpcHealthServer) poll(ctx context.Context, hs *grpchealth.Server) {
	s.sync(ctx, hs)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sync(ctx, hs)
		}
	}
}

// sync 运行就绪检查并更新整体健康状态。
func (s *grpcHealthServer) sync(ctx context.Context, hs *grpchealth.Server) {
	status := healthpb.HealthCheckResponse_SERVING
	if err := s.health.Readiness(ctx); err != nil {
		status = healthpb.HealthCheckResponse_NOT_SERVING
	}
	hs.SetServingStatus("", status)
}
