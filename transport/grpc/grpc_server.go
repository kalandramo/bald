package grpcserver

import (
	"context"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/transport"
)

// GRPCServer 封装 google.golang.org/grpc，实现 Server 契约。
//
// 本包只做协议：注册 gRPC 标准健康服务（`grpc.health.v1`）——它是 gRPC 生态的
// 协议标配，本包保证它在位，但**不判断业务就绪**。状态推入的入口经
// [GRPCServer.HealthServer] 交出，由装配层（`bootstrap.WithGRPCHealth` /
// `appkit.WithHealth`）或业务自行同步；reflection 同理由装配层按契约注册。
// 见《Bald 健康检查装配设计》。
//
// 嵌入 *grpc.Server 提升其方法（如 RegisterService），内部统一经嵌入字段操作。
//
// 并发安全：ln 由 mu 保护。Start 在 AppKit 的 errgroup goroutine 中执行，
// 而 Endpoint() 由 appkit 主 goroutine 轮询（waitForEndpoints）、Stop 由停机
// goroutine 调用，三者并发，无保护时构成数据竞争（go test -race 可复现）。
type GRPCServer struct {
	*grpc.Server
	cfg *bootstrapv1.Server_Grpc

	mu sync.RWMutex
	ln net.Listener // 实际监听器，用于解析 Endpoint

	// healthSrv 是本服务注册的标准健康服务实例（NewGRPCServer 自建 *grpc.Server
	// 时为 nil：那一路由业务自行注册）。仅供 HealthServer() 交出写入口。
	healthSrv *health.Server
}

// NewGRPCServer 基于已构建的 *grpc.Server 构造一个 GRPCServer。
// 注意：本构造不注册 health——业务自建 *grpc.Server 时自行注册。
func NewGRPCServer(cfg *bootstrapv1.Server_Grpc, srv *grpc.Server) *GRPCServer {
	return &GRPCServer{Server: srv, cfg: cfg}
}

// NewGRPCServerWithRegister 构造 gRPC 服务器并注册一个业务实现。
// register 回调用于把具体 Service 实现绑定到 gRPC Server。
// 同时注册 gRPC 标准健康服务（grpc.health.v1），初始状态 SERVING；
// 业务就绪状态经 [GRPCServer.HealthServer] 推入。
func NewGRPCServerWithRegister(
	cfg *bootstrapv1.Server_Grpc,
	unary []grpc.ServerOption,
	register func(s *grpc.Server),
) *GRPCServer {
	s := grpc.NewServer(unary...)
	hs := health.NewServer()
	healthpb.RegisterHealthServer(s, hs)
	if register != nil {
		register(s)
	}
	return &GRPCServer{Server: s, cfg: cfg, healthSrv: hs}
}

// HealthServer 返回本服务注册的 gRPC 标准健康服务实例；业务自建
// *grpc.Server（NewGRPCServer）时为 nil。
//
// 装配层用它把业务就绪状态同步给 gRPC 探针：
//
//	hs := srv.HealthServer()
//	hs.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
func (s *GRPCServer) HealthServer() *health.Server { return s.healthSrv }

// Options 返回该 server 直消费的 proto 配置，供宿主 introspection。
// 注意：transport.Server 契约（Start/Stop/Endpoint）不含 Options()，
// 这是 GRPCServer 的额外访问器。
func (s *GRPCServer) Options() any { return s.cfg }

// Start 启动 gRPC 服务器（阻塞）。
func (s *GRPCServer) Start(_ context.Context) error {
	lis, err := net.Listen("tcp", s.cfg.GetAddr())
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.ln = lis
	s.mu.Unlock()

	return s.Server.Serve(lis)
}

// Stop 优雅停止 gRPC 服务器（GracefulStop）；ctx 到期则强制 Stop。
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
// 通配符 / 仅端口绑定会被解析为本机可达 IP，确保注册到服务发现的 endpoint 可直连。
func (s *GRPCServer) Endpoint() string {
	// 先取出快照再解锁：Extract 内部会枚举网卡（net.Interfaces），
	// 是相对耗时的系统调用，不能在持锁期间执行。
	s.mu.RLock()
	ln := s.ln
	s.mu.RUnlock()

	if ln != nil {
		hostPort, err := transport.Extract(s.cfg.GetAddr(), ln)
		if err == nil {
			return "grpc://" + hostPort
		}
		return "grpc://" + ln.Addr().String()
	}
	// 未监听时返回空字符串，供 appkit.waitForEndpoints 正确等待 Start 真正执行。
	return ""
}
