package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/health"
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
// 返回值语义：出错短路；nil server 表示该协议未在契约中配置（跳过）；
// cleanup 释放资源，可为 nil。
//
// 与 go-wind 的差异：业务依赖（拦截器链、service 注册器、健康检查数据源）
// 经 Provider 工厂的 Option 显式注入，不用包级全局变量 + Bootstrap 前副作用调用。
type ServerProvider func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error)

// GRPCServerOption 配置 GRPCServerProvider 的业务依赖（显式注入，替代 go-wind 包级变量）。
type GRPCServerOption func(*grpcServerDeps)

type grpcServerDeps struct {
	unary          []grpc.ServerOption
	register       func(s *grpc.Server)
	health         *health.Health
	healthInterval time.Duration
	// unaryFn 是运行期求值的选项工厂（WithGRPCUnaryFunc）。在 provider 闭包内
	// （Run 期、server 构造时）调用，与静态 unary 累加——静态在前。
	unaryFn func() []grpc.ServerOption
}

// WithGRPCUnary 注入 gRPC 服务端选项（拦截器链、credentials 等）。
// 典型：bundle 归一化链（authn→audit→authz）经 bmiddlewaregrpc.ChainUnary 产出。
//
// 注意：opts 在**调用点**即求值（Go 变参语义）。若选项构造依赖运行期资源
// （认证器/授权器/仓储），请改用 [WithGRPCUnaryFunc]。
func WithGRPCUnary(opts ...grpc.ServerOption) GRPCServerOption {
	return func(d *grpcServerDeps) { d.unary = append(d.unary, opts...) }
}

// WithGRPCUnaryFunc 注入**延迟求值**的 gRPC 服务端选项。fn 在 provider 闭包内
// （Run 期、server 构造时）调用，故可读 beforeStart 建立的运行期资源——这是
// 应用层「构造期占位 + 请求期解析」代理的替代品。与 [WithGRPCUnary] 的静态选项
// 累加（静态在前、本项在后）。
func WithGRPCUnaryFunc(fn func() []grpc.ServerOption) GRPCServerOption {
	return func(d *grpcServerDeps) { d.unaryFn = fn }
}

// WithGRPCRegister 注入业务 service 注册回调（pb.RegisterXxxServer）。
func WithGRPCRegister(register func(s *grpc.Server)) GRPCServerOption {
	return func(d *grpcServerDeps) { d.register = register }
}

// WithGRPCHealth 注入健康检查数据源：非 nil 时框架启动后台轮询，把
// [health.Health] 的结果同步给 gRPC 标准健康服务（SERVING / NOT_SERVING），
// 使 K8s grpc 探针可见。interval <= 0 时用默认间隔（2s）。
//
// 协议实现不认识"就绪"：注册标准健康服务在 transport/grpc，判断与推送在
// 装配层（见《Bald 健康检查》）。
func WithGRPCHealth(h *health.Health, interval time.Duration) GRPCServerOption {
	return func(d *grpcServerDeps) {
		d.health = h
		d.healthInterval = interval
	}
}

// mergeUnary 合并静态与延迟求值的 unary 选项：**静态在前、延迟在后**。
//
// 链序是安全策略的一部分（如 ErrorInterceptor 须挂最外层），故顺序不可漂移；
// 抽成函数以便测试直接断言顺序（provider 的 deps 非导出，外部无法观测）。
// 拷贝静态切片，避免 append 污染 deps.unary（provider 可能被多次调用）。
func mergeUnary(deps *grpcServerDeps) []grpc.ServerOption {
	unary := deps.unary
	if deps.unaryFn == nil {
		return unary
	}
	return append(append([]grpc.ServerOption(nil), unary...), deps.unaryFn()...)
}

// GrpcServerProvider 返回 gRPC 协议服务器工厂（契约 Server.Grpc 段）。
// 契约段缺失时返回 nil server（引擎跳过该 provider 产物）。
//
// reflection（契约 Grpc.reflection）属"协议外能力"，在装配层按契约注册——
// transport/grpc 不再看这个字段；契约 true 时由框架注册，**业务不要在
// WithGRPCRegister 回调里重复注册**（同一服务重复注册会 panic）。
func GrpcServerProvider(opts ...GRPCServerOption) ServerProvider {
	deps := &grpcServerDeps{}
	for _, o := range opts {
		o(deps)
	}
	return func(_ context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		c := cfg.GetGrpc()
		if c == nil {
			return nil, nil, nil // 未配置 grpc，跳过
		}
		register := func(s *grpc.Server) {
			if c.GetReflection() {
				reflection.Register(s)
			}
			if deps.register != nil {
				deps.register(s)
			}
		}
		// 延迟求值的选项（WithGRPCUnaryFunc）在**此处**调用——本闭包即 server
		// 构造点（Run 期、业务 beforeStart 之后），故可读运行期资源。
		srv := grpcserver.NewGRPCServerWithRegister(c, mergeUnary(deps), register)
		if deps.health == nil {
			return srv, nil, nil
		}
		return NewGRPCHealthServer(srv, deps.health, deps.healthInterval), nil, nil
	}
}

// HTTPServerOption 配置 HTTPServerProvider 的业务依赖。
type HTTPServerOption func(*httpServerDeps)

type httpServerDeps struct {
	handler         http.Handler
	gatewayRegister func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error)
}

// WithHTTPHandler 注入业务 HTTP handler（gin.Engine 等，直接作为根 handler）。
// 探针（/healthz、/readyz）由业务 handler 自己拥有，或由装配层在其外层包装
// （appkit.WithHealth）——协议实现不注册任何框架路由。
func WithHTTPHandler(h http.Handler) HTTPServerOption {
	return func(d *httpServerDeps) { d.handler = h }
}

// WithGatewayRegister 注入 gateway 转码注册回调（pb.RegisterXxxHTTPServer 组合）。
// 非 nil 时走 grpc-gateway 反向代理模式（契约 Http.driver 或 provider 选择）。
func WithGatewayRegister(register func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error)) HTTPServerOption {
	return func(d *httpServerDeps) { d.gatewayRegister = register }
}

// HttpServerProvider 返回 HTTP 协议服务器工厂（契约 Server.Http 段）。
// 模式选择：deps 注入了 WithGatewayRegister 且契约 Http.driver 为
// "grpc-gateway"（或留空）时走 GatewayServer（gin 转码 + 反向代理），
// 否则走纯 HTTPServer（业务 handler 直挂）。
// 契约段缺失时返回 nil server（引擎跳过该 provider 产物）。
func HttpServerProvider(opts ...HTTPServerOption) ServerProvider {
	deps := &httpServerDeps{}
	for _, o := range opts {
		o(deps)
	}
	return func(_ context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		c := cfg.GetHttp()
		if c == nil {
			return nil, nil, nil // 未配置 http，跳过
		}
		if deps.gatewayRegister != nil && (c.GetDriver() == "" || c.GetDriver() == DriverGrpcGateway) {
			gw, err := gateway.NewGatewayServer(c, cfg.GetGrpc(), deps.gatewayRegister)
			if err != nil {
				return nil, nil, fmt.Errorf("bootstrap: build gateway server: %w", err)
			}
			return gw, nil, nil
		}
		return httpserver.NewHTTPServer(c, deps.handler), nil, nil
	}
}
