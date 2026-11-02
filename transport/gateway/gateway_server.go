package gateway

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconf/tlsconf"
	"github.com/kalandramo/bald/registry"
	"github.com/kalandramo/bald/transport/http"
)

// GatewayServer 是 grpc-gateway 反向代理：把 REST 请求转发到被代理的 gRPC 服务。
// 复用 HTTPServer 承载 mux，实现 Server 契约。
//
// 后端地址默认按配置直连；地址写成 `discovery:///<服务名>` 时必须提供发现来源
// （[WithDiscovery] 直给实例，或 [WithDiscoverySource] 延迟取），否则 Start
// 直接失败——见 [GatewayServer.resolveDiscovery]。
type GatewayServer struct {
	*httpserver.HTTPServer
	backend   *bootstrapv1.Server_Grpc // 被代理的 gRPC 配置（实时读 addr，避免构造期快照与 env 覆盖脱节）
	register  func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error)
	discovery registry.Discovery // 非空则注册 discovery:/// 解析器（见 WithDiscovery）
	// discoverySource 按地址决定是否调用：只有 backend addr 用 discovery scheme
	// 且未显式注入 Discovery 时才取（见 resolveDiscovery）。段驱动装配用它——
	// 装配作用域（App/Decl 上的注册中心）在构造期对契约层不可见，而调用方
	// 自带的 WithDiscovery 只能靠「谁被真正用上」裁决，不能靠选项顺序猜。
	discoverySource func() (registry.Discovery, error)
	conn            *grpc.ClientConn // 到后端 gRPC 的连接，Stop 时关闭
	closeOnce       sync.Once
	mu              sync.Mutex
}

// Option 是 GatewayServer 的构造选项。未设置的项一律保持「配置地址直连」的
// 既有行为，零开销。
type Option func(*GatewayServer)

// WithDiscovery 让 backend_grpc_addr 里的 `discovery:///<服务名>` 目标经
// registry.Discovery 解析端点，直连写法不受影响。
//
// 地址语义由字符串自己表达，这里不改契约字段类型：`Server_Grpc.addr` 仍是
// string，填 host:port 走原生 dns，填 discovery:///name 走注册中心。
//
// 解析出的多实例由 gRPC 默认 balancer（pick_first）按顺序取主备——这是故障转移
// 而非负载均衡；要多副本分摊流量，拨号侧需另配 round_robin 服务。
func WithDiscovery(d registry.Discovery) Option {
	return func(s *GatewayServer) { s.discovery = d }
}

// WithDiscoverySource 注入「需要时才取」的发现来源，用于装配期拿不到实例的接线
// （段驱动 Provider 在 Start 时才看地址，从已装配的注册中心取 Discovery）。
//
// 优先级低于 [WithDiscovery]：显式实例已在，不必再派生一份同源的。
func WithDiscoverySource(source func() (registry.Discovery, error)) Option {
	return func(s *GatewayServer) { s.discoverySource = source }
}

// NewGatewayServer 构造网关服务器。
//   - httpCfg: 网关监听地址与 TLS 配置（proto 的 Http，Tls.Enabled=false 为明文）
//   - backend: 被代理的 gRPC 服务配置（实时读 addr，故 env/flag 覆盖后也能生效）；
//     不能用固化字符串，否则构造期（config 加载前）会锁死默认端口，导致 env 覆盖失效。
//   - register: 业务自建 mux、注册 gateway handler 并返回一个 http.Handler；
//     返回 nil 时用默认的空 *http.ServeMux。register 为 nil 同理。
//
// 探针不在这里：转码 handler 由业务提供，探针由装配层在其返回值上包一层
// （appkit.WithHealth）或业务自己在 mux 上挂，见《Bald 健康检查装配设计》。
//
// register 的签名刻意是「返回 http.Handler」而不是「传入 *http.ServeMux」：
// grpc-gateway v2 用的是 runtime.ServeMux（不是标准库 *http.ServeMux），
// 业务自己创建并注册 mux、只交回一个 http.Handler，本包因此**无需依赖
// grpc-gateway**。
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
	opts ...Option,
) (*GatewayServer, error) {
	s := &GatewayServer{
		HTTPServer: httpserver.NewHTTPServer(httpCfg, nil),
		backend:    backend,
		register:   register,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// dialOpts 按被代理 gRPC 段的 tls 声明决定传输凭据：契约写了 tls 即 TLS，
// 否则明文。与被代理端读同一段配置，服务端开 TLS 后 gateway 不会连不上。
func dialOpts(tlsCfg *bootstrapv1.Server_TLS) ([]grpc.DialOption, error) {
	if tlsCfg == nil {
		return []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, nil
	}
	tc, err := tlsconf.Client(tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("gateway backend tls: %w", err)
	}
	return []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tc))}, nil
}

// resolveDiscovery 定下这次拨号用哪个发现来源：显式实例 > 延迟来源 > 无。
//
// 地址写了 discovery scheme 却取不到来源时在这里失败，而不是把目标交给 gRPC：
// 未注册的 scheme 会退回默认 dns resolver，一个必然拨不通的名字就被静默当成
// 主机名——失败要出现在拨号前。
//
// 延迟来源只在地址确实是 discovery:/// 时才调用：装配面上取注册中心不是零成本，
// 直连配置不该付这笔钱。
func (s *GatewayServer) resolveDiscovery() (registry.Discovery, error) {
	if s.discovery != nil {
		return s.discovery, nil
	}
	target := s.backend.GetAddr()
	if !usesDiscoveryTarget(target) {
		return nil, nil
	}
	if s.discoverySource == nil {
		return nil, fmt.Errorf("gateway: backend %q uses the %q scheme but no discovery is available (gateway.WithDiscovery / WithDiscoverySource)", target, DiscoveryScheme)
	}
	d, err := s.discoverySource()
	if err != nil {
		return nil, fmt.Errorf("gateway: discovery for backend %q: %w", target, err)
	}
	if d == nil {
		return nil, fmt.Errorf("gateway: discovery for backend %q: source returned nil", target)
	}
	return d, nil
}

// Start 在真正启动前建立到后端 gRPC 的连接并注册 handler（延迟解析地址）。
func (s *GatewayServer) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.conn != nil {
		s.mu.Unlock()
		return s.HTTPServer.Start(ctx)
	}
	opts, err := dialOpts(s.backend.GetTls())
	if err != nil {
		s.mu.Unlock()
		return err
	}
	d, err := s.resolveDiscovery()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if d != nil {
		opts = append(opts, grpc.WithResolvers(newDiscoveryBuilder(d)))
	}
	conn, err := grpc.NewClient(s.backend.GetAddr(), opts...)
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
