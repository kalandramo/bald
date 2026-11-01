package bootstrap

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/health"
	"github.com/kalandramo/bald/transport"
	grpcserver "github.com/kalandramo/bald/transport/grpc"
	httpserver "github.com/kalandramo/bald/transport/http"
)

// ServerProvider 是协议服务器工厂：从契约的 Server 配置构造 transport.Server。
//
// 返回值语义：出错短路；nil server 表示该协议未在契约中配置（跳过）；
// cleanup 释放资源，可为 nil。
//
// 业务依赖（拦截器链、service 注册器、健康检查数据源）经 Provider 工厂的 Option
// 显式注入，不用包级全局变量 + Bootstrap 前副作用调用。
type ServerProvider func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error)

// GRPCServerOption 配置 GRPCServerProvider 的业务依赖（显式注入，不用包级变量）。
type GRPCServerOption func(*grpcServerDeps)

type grpcServerDeps struct {
	// optionFuncs 是 gRPC 服务端选项工厂，**按登记顺序**在 server 构造点求值。
	// 只此一条路径：链序 = 登记顺序，没有「静态在前、延迟在后」的合并规则。
	optionFuncs    []func() []grpc.ServerOption
	register       func(s *grpc.Server)
	health         *health.Health
	healthInterval time.Duration
}

// WithGRPCUnaryFunc 注入 gRPC 服务端选项工厂（拦截器链、credentials 等）。
// fn 在 provider 闭包内（Run 期、server 构造时）调用，故可读 beforeStart
// 建立的运行期资源（认证器/授权器/仓储）。多次登记按登记顺序累加。
//
// 典型：bundle 归一化链（authn→audit→authz）经 bmiddlewaregrpc.ChainUnary 产出。
// 注意链序是安全策略的一部分（ErrorInterceptor 须在最外层），登记顺序即链序。
func WithGRPCUnaryFunc(fn func() []grpc.ServerOption) GRPCServerOption {
	return func(d *grpcServerDeps) {
		if fn != nil {
			d.optionFuncs = append(d.optionFuncs, fn)
		}
	}
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

// applyOptions 按登记顺序求值各选项工厂并串成一条链。
//
// 链序是安全策略的一部分（如 ErrorInterceptor 须挂最外层），故顺序不可漂移；
// 抽成函数以便测试直接断言（provider 的 deps 非导出，外部无法观测）。
func applyOptions(deps *grpcServerDeps) []grpc.ServerOption {
	var opts []grpc.ServerOption
	for _, fn := range deps.optionFuncs {
		opts = append(opts, fn()...)
	}
	return opts
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
		// 选项工厂在**此处**求值——本闭包即 server 构造点（Run 期、业务
		// beforeStart 之后），故工厂内可读运行期资源。
		srv := grpcserver.NewGRPCServerWithRegister(c, applyOptions(deps), register)
		if deps.health == nil {
			return srv, nil, nil
		}
		return NewGRPCHealthServer(srv, deps.health, deps.healthInterval), nil, nil
	}
}

// HTTPServerOption 配置 HTTPServerProvider 的业务依赖。
type HTTPServerOption func(*httpServerDeps)

type httpServerDeps struct {
	handler http.Handler
}

// WithHTTPHandler 注入业务 HTTP handler（gin.Engine 等，直接作为根 handler）。
// 探针（/healthz、/readyz）由业务 handler 自己拥有，或由装配层在其外层包装
// （appkit.WithHealth）——协议实现不注册任何框架路由。
func WithHTTPHandler(h http.Handler) HTTPServerOption {
	return func(d *httpServerDeps) { d.handler = h }
}

// HttpServerProvider 返回 HTTP 协议服务器工厂（契约 Server.Http 段）。
// 契约段缺失时返回 nil server（引擎跳过该 provider 产物）。
//
// grpc-gateway 转码面不在这里：本包不 import 任何具体后端之外的转码实现，
// 同端口单面的转码拓扑由 transport/gateway/contract 的 HTTPProvider 接管
// server.http 段表达（见《Bald 传输层设计》）。
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
		return httpserver.NewHTTPServer(c, deps.handler), nil, nil
	}
}
