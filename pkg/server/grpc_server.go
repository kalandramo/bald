package server

import (
	"context"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	confv1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/config/v1"
)

// defaultReadinessPollInterval 是 gRPC 侧后台轮询 readiness 回调的默认间隔。
// gRPC health 是拉模型（探针主动 Check），无法由业务主动推状态，
// 因此由本 goroutine 周期性运行 readiness 并 SetServingStatus 同步健康状态。
// 可用 WithReadinessPollInterval 覆盖（服务端设计 §7.2 技术债落地）。
const defaultReadinessPollInterval = 2 * time.Second

// GRPCServerOption 配置 GRPCServer。
type GRPCServerOption func(*GRPCServer)

// WithReadinessPollInterval 覆盖 gRPC 侧 readiness 后台轮询间隔（默认 2s）。
// 业务依赖检测较慢（如健康检查需打到外部系统）时可调大，降低探测频率。
func WithReadinessPollInterval(d time.Duration) GRPCServerOption {
	return func(s *GRPCServer) {
		if d > 0 {
			s.readinessInterval = d
		}
	}
}

// GRPCServer 封装 google.golang.org/grpc，实现 Server 契约。
// 默认注册 health check 与 reflection，方便运维探测与调试。
// 嵌入 *grpc.Server 提升其方法（如 RegisterService），内部统一经嵌入字段操作。
//
// 并发安全：ln 与 readinessCancel 由 mu 保护。
// Start 在 AppKit 的 errgroup goroutine 中执行，而 Endpoint() 由 appkit 主
// goroutine 轮询（waitForEndpoints）、Stop 由停机 goroutine 调用，三者并发，
// 无保护时构成数据竞争（go test -race 可复现）。
type GRPCServer struct {
	*grpc.Server
	cfg *confv1.Grpc

	// mu 保护下方 ln 与 readinessCancel 的并发读写。
	mu              sync.RWMutex
	ln              net.Listener // 实际监听器，用于解析 Endpoint
	readinessCancel context.CancelFunc

	healthSrv         *health.Server // 保存引用，用于 readiness 联动（SetServingStatus）
	readiness         ReadinessFunc  // 可为 nil（nil 时 health 恒 SERVING）
	readinessInterval time.Duration  // readiness 轮询间隔（默认 defaultReadinessPollInterval）
}

// NewGRPCServer 基于已构建的 *grpc.Server 构造一个 GRPCServer。
// readiness 为可选的就绪探针回调：传 nil 时 health 状态恒 SERVING（仅作存活）。
// opts 可覆盖 readiness 轮询间隔（WithReadinessPollInterval）。
// 注意：本构造不入 health/reflection（业务自建 *grpc.Server 时自行注册）。
func NewGRPCServer(
	cfg *confv1.Grpc,
	srv *grpc.Server,
	readiness ReadinessFunc,
	opts ...GRPCServerOption,
) *GRPCServer {
	g := &GRPCServer{
		Server:            srv,
		cfg:               cfg,
		readiness:         readiness,
		readinessInterval: defaultReadinessPollInterval,
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// NewGRPCServerWithRegister 构造 gRPC 服务器并注册一个业务实现。
// register 回调用于把具体 Service 实现绑定到 gRPC Server。
// readiness 为可选的就绪探针回调（与 HTTP /readyz 对称）；非 nil 时启动后台轮询，
// 将结果通过 health.SetServingStatus 同步给 gRPC health 协议，使 K8s grpc 探针可见。
func NewGRPCServerWithRegister(
	cfg *confv1.Grpc,
	unary []grpc.ServerOption,
	register func(s *grpc.Server),
	readiness ReadinessFunc,
	opts ...GRPCServerOption,
) *GRPCServer {
	s := grpc.NewServer(unary...)
	hs := health.NewServer()
	healthpb.RegisterHealthServer(s, hs)
	reflection.Register(s)
	if register != nil {
		register(s)
	}
	g := &GRPCServer{
		Server:            s,
		cfg:               cfg,
		healthSrv:         hs,
		readiness:         readiness,
		readinessInterval: defaultReadinessPollInterval,
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// Options 返回该 server 直消费的 proto 配置（实现 server.Server 契约的 Options()）。
func (s *GRPCServer) Options() any { return s.cfg }

// Start 启动 gRPC 服务器（阻塞）。若配置了 readiness，同时启动后台轮询，
// 周期性把 readiness 结果同步到 health 状态（SERVING / NOT_SERVING）。
func (s *GRPCServer) Start(ctx context.Context) error {
	lis, err := listen(s.cfg.GetAddr())
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.ln = lis
	s.mu.Unlock()

	// readiness 轮询放在 listen 成功之后：否则 listen 失败时 goroutine 已启动却
	// 无人负责取消（Start 返回错误后调用方未必再调 Stop），造成 goroutine 泄漏。
	if s.readiness != nil && s.healthSrv != nil {
		rctx, cancel := context.WithCancel(context.Background())
		s.mu.Lock()
		s.readinessCancel = cancel
		s.mu.Unlock()
		go s.pollReadiness(rctx)
	}

	return s.Server.Serve(lis)
}

// pollReadiness 周期性调用 readiness 并同步 health 状态。
// 启动时立即探一次（尽快暴露未就绪），之后按间隔轮询。
func (s *GRPCServer) pollReadiness(ctx context.Context) {
	s.syncHealth(ctx) // 立即探一次
	ticker := time.NewTicker(s.readinessInterval)
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
	s.mu.RLock()
	cancel := s.readinessCancel
	s.mu.RUnlock()
	if cancel != nil {
		cancel()
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
	// 先取出快照再解锁：Extract 内部会枚举网卡（net.Interfaces），
	// 是相对耗时的系统调用，不能在持锁期间执行。
	s.mu.RLock()
	ln := s.ln
	s.mu.RUnlock()

	if ln != nil {
		hostPort, err := Extract(s.cfg.GetAddr(), ln)
		if err == nil {
			return "grpc://" + hostPort
		}
		return "grpc://" + ln.Addr().String()
	}
	// 未监听时返回空字符串，供 appkit.waitForEndpoints 正确等待 Start 真正执行。
	return ""
}
