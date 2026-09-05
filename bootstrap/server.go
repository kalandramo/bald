package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/transport"
	gateway "github.com/kalandramo/bald/transport/gateway"
	grpcserver "github.com/kalandramo/bald/transport/grpc"
	httpserver "github.com/kalandramo/bald/transport/http"
)

// DriverGrpcGateway 是契约 Http.driver 的 grpc-gateway 模式值。
// 留空时若注入了 WithGatewayRegister 也按此模式装配（向后兼容默认值）。
const DriverGrpcGateway = "grpc-gateway"

// ServerProvider 是协议服务器工厂：从契约的 Server 配置构造 transport.Server。
//
// 返回值语义与 LoggerProvider 一致：出错短路；nil server 视为无法处理该配置
// （服务器是必需品，BuildServers 会报错而非跳过）；cleanup 释放资源，可为 nil。
//
// 与 go-wind 的差异：业务依赖（拦截器链、service 注册器、探针路径）经
// Provider 工厂的 Option 显式注入，不用包级全局变量 + Bootstrap 前副作用调用。
type ServerProvider func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error)

// ServerRegistry 按名字注册协议服务器工厂（显式注册，无 init() 副作用）。
// 名字约定小写（"grpc"/"http"），与契约 Server 段名对应。
type ServerRegistry struct {
	mu        sync.RWMutex
	providers map[string]ServerProvider
}

// NewServerRegistry 创建一个空的 [ServerRegistry]。
func NewServerRegistry() *ServerRegistry {
	return &ServerRegistry{providers: make(map[string]ServerProvider)}
}

// Register 注册一个协议服务器工厂。重名报错、不覆盖（fail-fast）。
func (r *ServerRegistry) Register(name string, p ServerProvider) error {
	if name == "" {
		return fmt.Errorf("bootstrap: server provider name is empty")
	}
	if p == nil {
		return fmt.Errorf("bootstrap: server provider %q is nil", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[name]; ok {
		return fmt.Errorf("bootstrap: server provider %q already registered", name)
	}
	r.providers[name] = p
	return nil
}

// MustRegister 是 [ServerRegistry.Register] 的 panic 版本，仅用于主程序 main() 内显式注册。
func (r *ServerRegistry) MustRegister(name string, p ServerProvider) {
	if err := r.Register(name, p); err != nil {
		panic(err)
	}
}

// BuildServers 按契约已配置的协议段构造全部服务器（**多选语义**）。
//
// 与日志（type 单选）、配置源（级联序）不同：grpc/http 段可同时配置、同时生效，
// 因此遍历全部已注册 provider，逐个调用——provider 自行判断契约中自己的段
// 是否存在（未配置返回 nil server，跳过；与日志不同，这里 nil=跳过是合法语义，
// 因为「只配了 http」时 grpc provider 返回 nil 是预期而非错误）。
//
// 返回 ([]transport.Server, cleanup, error)：cleanup 逆序释放全部服务器资源；
// 任一 provider 出错时短路并回滚已构造的服务器。
// 全部 provider 都返回 nil（一个协议都没配）时返回错误——进程至少要监听一个端口。
func (r *ServerRegistry) BuildServers(ctx context.Context, cfg *bootstrapv1.Server) ([]transport.Server, func(), error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("bootstrap: server config is nil")
	}

	r.mu.RLock()
	names := make([]string, 0, len(r.providers))
	for k := range r.providers {
		names = append(names, k)
	}
	providers := make(map[string]ServerProvider, len(r.providers))
	for k, v := range r.providers {
		providers[k] = v
	}
	r.mu.RUnlock()
	sort.Strings(names) // 确定性顺序（错误信息与遍历序稳定）

	var (
		servers []transport.Server
		closers []func()
	)
	for _, name := range names {
		srv, closer, err := providers[name](ctx, cfg)
		if err != nil {
			runClosers(closers)
			return nil, nil, fmt.Errorf("bootstrap: server provider %s: %w", name, err)
		}
		if srv == nil {
			continue // 该协议未在契约中配置，跳过
		}
		servers = append(servers, srv)
		if closer != nil {
			closers = append(closers, closer)
		}
	}
	if len(servers) == 0 {
		return nil, nil, fmt.Errorf("bootstrap: no server configured (registered: %v)", names)
	}
	return servers, func() { runClosers(closers) }, nil
}

// GRPCServerOption 配置 GRPCServerProvider 的业务依赖（显式注入，替代 go-wind 包级变量）。
type GRPCServerOption func(*grpcServerDeps)

type grpcServerDeps struct {
	unary       []grpc.ServerOption
	register    func(s *grpc.Server)
	readiness   transport.ReadinessFunc
	pollIntervl *int
}

// WithGRPCUnary 注入 gRPC 服务端选项（拦截器链、credentials 等）。
// 典型：bundle 归一化链（authn→audit→authz）经 bmiddlewaregrpc.ChainUnary 产出。
func WithGRPCUnary(opts ...grpc.ServerOption) GRPCServerOption {
	return func(d *grpcServerDeps) { d.unary = append(d.unary, opts...) }
}

// WithGRPCRegister 注入业务 service 注册回调（pb.RegisterXxxServer）。
func WithGRPCRegister(register func(s *grpc.Server)) GRPCServerOption {
	return func(d *grpcServerDeps) { d.register = register }
}

// WithGRPCReadiness 注入就绪探针回调（联动 gRPC health 状态）。
func WithGRPCReadiness(fn transport.ReadinessFunc) GRPCServerOption {
	return func(d *grpcServerDeps) { d.readiness = fn }
}

// WithGRPCPollInterval 覆盖 readiness 轮询间隔（默认 2s）。
func WithGRPCPollInterval(d int) GRPCServerOption { //nolint:unparam // 预留
	return func(d2 *grpcServerDeps) { i := d; d2.pollIntervl = &i }
}

// GrpcServerProvider 返回 gRPC 协议服务器工厂（契约 Server.Grpc 段）。
// 契约段缺失时返回 nil server（BuildServers 跳过）；
// reflection 由契约 Grpc.reflection 字段控制（默认关闭）。
func GrpcServerProvider(opts ...GRPCServerOption) ServerProvider {
	deps := &grpcServerDeps{}
	for _, o := range opts {
		o(deps)
	}
	serverOpts := make([]grpcserver.GRPCServerOption, 0, 2)
	if deps.pollIntervl != nil {
		serverOpts = append(serverOpts, grpcserver.WithReadinessPollInterval(time.Duration(*deps.pollIntervl)))
	}
	return func(_ context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		c := cfg.GetGrpc()
		if c == nil {
			return nil, nil, nil // 未配置 grpc，跳过
		}
		srv := grpcserver.NewGRPCServerWithRegister(c, deps.unary, deps.register, deps.readiness, serverOpts...)
		return srv, nil, nil
	}
}

// HTTPServerOption 配置 HTTPServerProvider / GatewayServerProvider 的业务依赖。
type HTTPServerOption func(*httpServerDeps)

type httpServerDeps struct {
	handler         http.Handler
	gatewayRegister func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error)
	readiness       transport.ReadinessFunc
	probeHealthPath string
	probeReadyPath  string
}

// WithHTTPHandler 注入业务 HTTP handler（gin.Engine 等，直接作为根 handler）。
func WithHTTPHandler(h http.Handler) HTTPServerOption {
	return func(d *httpServerDeps) { d.handler = h }
}

// WithGatewayRegister 注入 gateway 转码注册回调（pb.RegisterXxxHTTPServer 组合）。
// 非 nil 时走 grpc-gateway 反向代理模式（契约 Http.driver 或 provider 选择）。
func WithGatewayRegister(register func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error)) HTTPServerOption {
	return func(d *httpServerDeps) { d.gatewayRegister = register }
}

// WithHTTPReadiness 注入就绪探针回调（驱动 /readyz）。
func WithHTTPReadiness(fn transport.ReadinessFunc) HTTPServerOption {
	return func(d *httpServerDeps) { d.readiness = fn }
}

// WithProbePaths 覆盖探针路径（默认 /healthz /readyz）。
func WithProbePaths(health, ready string) HTTPServerOption {
	return func(d *httpServerDeps) { d.probeHealthPath, d.probeReadyPath = health, ready }
}

// HttpServerProvider 返回 HTTP 协议服务器工厂（契约 Server.Http 段）。
// 模式选择：deps 注入了 WithGatewayRegister 且契约 Http.driver 为
// "grpc-gateway"（或留空）时走 GatewayServer（gin 转码 + 反向代理），
// 否则走纯 HTTPServer（业务 handler 直挂）。
// 契约段缺失时返回 nil server（BuildServers 跳过）。
func HttpServerProvider(opts ...HTTPServerOption) ServerProvider {
	deps := &httpServerDeps{}
	for _, o := range opts {
		o(deps)
	}
	serverOpts := make([]httpserver.HTTPServerOption, 0, 2)
	if deps.probeHealthPath != "" || deps.probeReadyPath != "" {
		serverOpts = append(serverOpts, httpserver.WithProbePaths(deps.probeHealthPath, deps.probeReadyPath))
	}
	return func(_ context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		c := cfg.GetHttp()
		if c == nil {
			return nil, nil, nil // 未配置 http，跳过
		}
		if deps.gatewayRegister != nil && (c.GetDriver() == "" || c.GetDriver() == DriverGrpcGateway) {
			gw, err := gateway.NewGatewayServer(c, cfg.GetGrpc(), deps.gatewayRegister, deps.readiness)
			if err != nil {
				return nil, nil, fmt.Errorf("bootstrap: build gateway server: %w", err)
			}
			return gw, nil, nil
		}
		handler := deps.handler
		if handler == nil {
			handler = http.NotFoundHandler()
		}
		return httpserver.NewHTTPServer(c, handler, deps.readiness, serverOpts...), nil, nil
	}
}
