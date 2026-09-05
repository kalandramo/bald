package gateway

import (
	"context"
	"net/http"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/transport"
	"github.com/kalandramo/bald/transport/http"
)

// GatewayServer 是 grpc-gateway 反向代理：将 REST 请求转发到本地 gRPC 服务。
// 复用 HTTPServer 承载 mux，实现 Server 契约。
type GatewayServer struct {
	*httpserver.HTTPServer
	backend   *bootstrapv1.Server_Grpc // 被代理的 gRPC 配置（实时读 addr，避免构造期快照与 env 覆盖脱节）
	register  func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error)
	conn      *grpc.ClientConn // 到后端 gRPC 的连接，Stop 时关闭
	closeOnce sync.Once
	mu        sync.Mutex
}

// NewGatewayServer 构造网关服务器。
//   - httpCfg: 网关监听地址与 TLS 配置（proto 的 Http，Tls.Enabled=false 为明文）
//   - backend: 被代理的 gRPC 服务配置（实时读 addr，故 env/flag 覆盖后也能生效）；
//     不能用固化字符串，否则构造期（config 加载前）会锁死默认端口，导致 env 覆盖失效。
//   - register: 业务自建 mux、注册 gateway handler 并返回一个 http.Handler；
//     返回 nil 时用默认的空 *http.ServeMux。register 为 nil 同理。
//   - readiness: 复用与 HTTP 对称的就绪探针；nil 时 /readyz 恒 200
//
// register 的签名刻意是「返回 http.Handler」而不是「传入 *http.ServeMux」：
// grpc-gateway v2 用的是 runtime.ServeMux（不是标准库 *http.ServeMux），
// 旧签名把 *http.ServeMux 硬编码进去，导致 grpc-gateway v2 根本无法接入。
// 改成让业务自己创建并注册、只交回一个 http.Handler 后，
// 本包**无需依赖 grpc-gateway**（与 P5「核心零后端依赖」治理一致）。
//
// conn 与 handler 延迟到 Start 时才建立：构造期 config 尚未加载，backend.addr
// 仍是默认值；延迟到 Start（config 已就绪）可拿到 env/flag 覆盖后的真实地址。
//
// 业务侧用法：
//
//	func registerGateway(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
//	    mux := runtime.NewServeMux()          // grpc-gateway v2
//	    if err := baldv1.RegisterGreetServiceHandler(ctx, mux, conn); err != nil {
//	        return nil, err
//	    }
//	    return mux, nil                        // runtime.ServeMux 实现 http.Handler
//	}
func NewGatewayServer(
	httpCfg *bootstrapv1.Server_Http,
	backend *bootstrapv1.Server_Grpc,
	register func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error),
	readiness transport.ReadinessFunc,
) (*GatewayServer, error) {
	return &GatewayServer{
		HTTPServer: httpserver.NewHTTPServer(httpCfg, nil, readiness),
		backend:    backend,
		register:   register,
	}, nil
}

// Start 在真正启动前建立到后端 gRPC 的连接并注册 handler（延迟解析地址）。
func (s *GatewayServer) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.conn != nil {
		s.mu.Unlock()
		return s.HTTPServer.Start(ctx)
	}
	conn, err := grpc.NewClient(s.backend.GetAddr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		s.mu.Unlock()
		return err
	}
	handler := http.Handler(http.NewServeMux())
	if s.register != nil {
		h, err := s.register(ctx, conn)
		if err != nil {
			_ = conn.Close()
			s.mu.Unlock()
			return err
		}
		if h != nil {
			handler = h
		}
	}
	s.conn = conn
	s.HTTPServer.AttachHandler(handler)
	// 建立阶段结束，先释放锁再进入阻塞的 Serve：
	// 否则 Stop 需要同把锁才能调 Shutdown，会形成「Stop 等 Start 释放锁、
	// Start 等 Stop 调 Shutdown」的死锁。
	s.mu.Unlock()
	return s.HTTPServer.Start(ctx)
}

// Stop 停止网关时同时关闭到后端 gRPC 的连接。
func (s *GatewayServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		defer s.closeOnce.Do(func() { _ = s.conn.Close() })
	}
	return s.HTTPServer.Stop(ctx)
}
