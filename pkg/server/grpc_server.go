package server

import (
	"context"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	baldoptions "github.com/kalandramo/bald/pkg/options"
)

// readinessPollInterval 是 gRPC 侧后台轮询 readiness 回调的间隔。
// gRPC health 是拉模型（探针主动 Check），无法由业务主动推状态，
// 因此由本 goroutine 周期性运行 readiness 并 SetServingStatus 同步健康状态。
const readinessPollInterval = 2 * time.Second

// GRPCServer 封装 google.golang.org/grpc，实现 Server 契约。
// 默认注册 health check 与 reflection，方便运维探测与调试。
// 嵌入 *grpc.Server 提升其方法（如 RegisterService），内部统一经嵌入字段操作。
type GRPCServer struct {
	*grpc.Server
	opts *baldoptions.GRPCOptions
	addr string
	ln   net.Listener // 实际监听器，用于解析 Endpoint

	healthSrv       *health.Server // 保存引用，用于 readiness 联动（SetServingStatus）
	readiness       ReadinessFunc  // 可为 nil（nil 时 health 恒 SERVING）
	readinessCancel context.CancelFunc
}

// NewGRPCServer 基于已构建的 *grpc.Server 构造一个 GRPCServer。
// readiness 为可选的就绪探针回调：传 nil 时 health 状态恒 SERVING（仅作存活）。
func NewGRPCServer(opts *baldoptions.GRPCOptions, srv *grpc.Server, readiness ReadinessFunc) *GRPCServer {
	return &GRPCServer{
		Server:    srv,
		opts:      opts,
		addr:      opts.Addr,
		readiness: readiness,
	}
}

// NewGRPCServerWithRegister 构造 gRPC 服务器并注册一个业务实现。
// register 回调用于把具体 Service 实现绑定到 gRPC Server。
// readiness 为可选的就绪探针回调（与 HTTP /readyz 对称）；非 nil 时启动后台轮询，
// 将结果通过 health.SetServingStatus 同步给 gRPC health 协议，使 K8s grpc 探针可见。
func NewGRPCServerWithRegister(
	opts *baldoptions.GRPCOptions,
	unary []grpc.ServerOption,
	register func(s *grpc.Server),
	readiness ReadinessFunc,
) *GRPCServer {
	s := grpc.NewServer(unary...)
	hs := health.NewServer()
	healthpb.RegisterHealthServer(s, hs)
	reflection.Register(s)
	if register != nil {
		register(s)
	}
	return &GRPCServer{
		Server:    s,
		opts:      opts,
		addr:      opts.Addr,
		healthSrv: hs,
		readiness: readiness,
	}
}

// Start 启动 gRPC 服务器（阻塞）。若配置了 readiness，同时启动后台轮询，
// 周期性把 readiness 结果同步到 health 状态（SERVING / NOT_SERVING）。
func (s *GRPCServer) Start(ctx context.Context) error {
	if s.readiness != nil && s.healthSrv != nil {
		rctx, cancel := context.WithCancel(context.Background())
		s.readinessCancel = cancel
		go s.pollReadiness(rctx)
	}
	lis, err := listen(s.addr)
	if err != nil {
		return err
	}
	s.ln = lis
	return s.Server.Serve(lis)
}

// pollReadiness 周期性调用 readiness 并同步 health 状态。
// 启动时立即探一次（尽快暴露未就绪），之后按间隔轮询。
func (s *GRPCServer) pollReadiness(ctx context.Context) {
	s.syncHealth(ctx) // 立即探一次
	ticker := time.NewTicker(readinessPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncHealth(ctx)
		}
	}
}

// syncHealth 运行 readiness 回调并更新 health 状态。
func (s *GRPCServer) syncHealth(ctx context.Context) {
	status := healthpb.HealthCheckResponse_SERVING
	if err := s.readiness(ctx); err != nil {
		status = healthpb.HealthCheckResponse_NOT_SERVING
	}
	s.healthSrv.SetServingStatus("", status) // "" = 整体服务（非具体子服务）
}

// Stop 优雅停止 gRPC 服务器（GracefulStop），并取消 readiness 轮询 goroutine。
func (s *GRPCServer) Stop(ctx context.Context) error {
	if s.readinessCancel != nil {
		s.readinessCancel()
	}
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
// 通配符 / 仅端口绑定会被解析为本机可达 IP，确保注册到服务发现的 endpoint 可直连。
func (s *GRPCServer) Endpoint() string {
	if s.ln != nil {
		hostPort, err := Extract(s.addr, s.ln)
		if err == nil {
			return "grpc://" + hostPort
		}
		return "grpc://" + s.ln.Addr().String()
	}
	// 未监听时返回空字符串，供 appkit.waitForEndpoints 正确等待 Start 真正执行。
	return ""
}
