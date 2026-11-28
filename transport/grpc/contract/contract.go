// Package contract 提供 gRPC 协议服务器的契约装配：bootstrapv1.Server 的 grpc 段
// → transport/grpc 构造参数映射 + 业务依赖注入（拦截器链、service 注册、健康
// 数据源），供 server.grpc 段 Provider 的 Build 复用。
//
// 为什么在本包而不是 bootstrap：见 transport/http/contract 的同款说明（评审
// N-1）——「契约段 → 具体 transport 实现」的知识只有一个落点，kernel 不认识
// 具体传输实现。reflection 这类「协议外能力」也随本包消费：契约
// Grpc.reflection=true 时由本包注册，transport/grpc 不看该字段。
//
// 与 transport/grpc 的关系：实现根包保持零契约依赖，只有本包 import bconf；
// 标准健康服务**注册**留在 transport/grpc，就绪状态**推送**在本包（health.go）
// ——协议只承诺协议形状，判稳与推送属装配层（见《Bald 健康检查装配设计》）。
//
// 与装配层的关系：本包既不 import pkg/appkit 也不 import bootstrap，Provider
// 返回**未命名函数类型**，调用方直接调用取回 transport.Server。
package contract

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/health"

	"github.com/kalandramo/bald/transport"
	grpcserver "github.com/kalandramo/bald/transport/grpc"
)

// Option 配置本 Provider 的业务依赖（显式注入，不用包级变量）。
type Option func(*providerConfig)

type providerConfig struct {
	// optionFuncs 是 gRPC 服务端选项工厂，**按登记顺序**在 server 构造点求值。
	// 只此一条路径：链序 = 登记顺序，没有「静态在前、延迟在后」的合并规则。
	optionFuncs    []func() []grpc.ServerOption
	register       func(s *grpc.Server)
	health         *health.Health
	healthInterval time.Duration
}

// WithUnaryFunc 注入 gRPC 服务端选项工厂（拦截器链、credentials 等）。
// fn 在 provider 闭包内（Run 期、server 构造时）调用，故可读 beforeStart
// 建立的运行期资源（认证器/授权器/仓储）。多次登记按登记顺序累加。
//
// 典型：bundle 归一化链（authn→audit→authz）经 bmiddlewaregrpc.ChainUnary 产出。
// 注意链序是安全策略的一部分（ErrorInterceptor 须在最外层），登记顺序即链序。
func WithUnaryFunc(fn func() []grpc.ServerOption) Option {
	return func(c *providerConfig) {
		if fn != nil {
			c.optionFuncs = append(c.optionFuncs, fn)
		}
	}
}

// WithRegister 注入业务 service 注册回调（pb.RegisterXxxServer）。
func WithRegister(register func(s *grpc.Server)) Option {
	return func(c *providerConfig) { c.register = register }
}

// WithHealth 注入健康检查数据源：非 nil 时框架启动后台轮询，把
// [health.Health] 的结果同步给 gRPC 标准健康服务（SERVING / NOT_SERVING），
// 使 K8s grpc 探针可见。interval <= 0 时用默认间隔（2s）。
func WithHealth(h *health.Health, interval time.Duration) Option {
	return func(c *providerConfig) {
		c.health = h
		c.healthInterval = interval
	}
}

// applyOptions 按登记顺序求值各选项工厂并串成一条链。
//
// 链序是安全策略的一部分（如 ErrorInterceptor 须挂最外层），故顺序不可漂移；
// 抽成函数以便测试直接断言（provider 的 deps 非导出，外部无法观测）。
func applyOptions(deps *providerConfig) []grpc.ServerOption {
	var opts []grpc.ServerOption
	for _, fn := range deps.optionFuncs {
		opts = append(opts, fn()...)
	}
	return opts
}

// Provider 返回契约驱动的 gRPC 服务器工厂（契约 Server.Grpc 段）。
// 契约段缺失时返回 nil server（引擎跳过该 provider 产物）。
//
// reflection（契约 Grpc.reflection）属"协议外能力"，在本包按契约注册——
// transport/grpc 不看这个字段；契约 true 时由框架注册，**业务不要在
// WithRegister 回调里重复注册**（同一服务重复注册会 panic）。
func Provider(opts ...Option) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	deps := &providerConfig{}
	for _, o := range opts {
		o(deps)
	}
	return func(_ context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		sec := cfg.GetGrpc()
		if sec == nil {
			return nil, nil, nil // 未配置 grpc，跳过
		}
		register := func(s *grpc.Server) {
			if sec.GetReflection() {
				reflection.Register(s)
			}
			if deps.register != nil {
				deps.register(s)
			}
		}
		// 选项工厂在**此处**求值——本闭包即 server 构造点（Run 期、业务
		// beforeStart 之后），故工厂内可读运行期资源。
		srv := grpcserver.NewGRPCServerWithRegister(sec, applyOptions(deps), register)
		if deps.health == nil {
			return srv, nil, nil
		}
		return NewHealthServer(srv, deps.health, deps.healthInterval), nil, nil
	}
}

// 类型守卫：Provider 保持构造 func 形状（调用方在 server.grpc 段 Provider 的
// Build 里直接调用，本包不必 import bootstrap/appkit 的任何类型）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
