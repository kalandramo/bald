// 传输面 Option 构造器域（ADR-8 第二阶段拆分自 bootstrap.go）：WithHTTP/
// WithGRPC 系是**声明面**（写进 Decl，装配在 assemble）——handler 在装配期
// 即定型、契约钩子在 Run 期求值，各 Option 的时序承诺见各自注释。
package appkit

import (
	"context"
	"fmt"
	"net/http"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/transport"
	"google.golang.org/grpc"
)

// WithHTTP 声明启用 HTTP 服务器，业务 handler 必供（探针路径由框架兜底）。
//
// handler 在装配期即定型，因此看不到契约——要按 server.http.middleware 段配
// 横切链用 [WithHTTPContract]。传 nil = 撤掉 HTTP 面（变体让位转码面用这条），
// 同时清掉契约钩子声明，否则钩子会把面装回来。
func WithHTTP(handler http.Handler) Option {
	return optionFunc(func(s *Decl) {
		s.HTTPHandler = handler
		if handler == nil {
			s.HTTPContract = nil
		}
	})
}

// HTTPContractFunc 是 HTTP 面的契约钩子：在契约装载后、域接线与 server 构造
// 之前求值一次（见 contract_faces.go 的求值时点说明）。
//
// 入参 mw 是契约 server.http.middleware 段（nil = 未配置该段），可直接喂
// bundle.FromMiddleware；返回 (handler, close, err)——close 释放钩子构造的资源
// （如桥新建的限流器，可为 nil，框架挂成停机 Effect 逆序回放），err 非 nil
// 即启动失败。middleware 段属静态面：热更不重求值本钩子，改配置要重启。
type HTTPContractFunc func(mw *bootstrapv1.Server_Http_Middleware) (handler http.Handler, close func() error, err error)

// WithHTTPContract 声明启用 HTTP 服务器，并把 handler 的构造延到契约装载之后。
//
// 这是契约 server.http.middleware 段的正式消费口：横切链的参数（限流桶容量、
// 超时预算、CORS 白名单、日志跳过路径…）来自配置文件，而链本身与认证姿态仍是
// 代码里的显式声明——框架不代替业务决定「公开还是接认证」（契约里没有 authn 段）。
//
// 与 [WithHTTP] 二选一：两条口同时给 = 启动报错（不是末者胜，避免「配了链
// 却被另一条覆盖」）。
func WithHTTPContract(fn HTTPContractFunc) Option {
	return optionFunc(func(s *Decl) { s.HTTPContract = fn })
}

// GRPCOptionsContractFunc 是 gRPC 服务端选项的契约钩子。
//
// 入参 mw 是契约 server.grpc.middleware 段（nil = 未配置该段），可直接喂
// bundle.FromGRPCMiddleware；其余契约与 [HTTPContractFunc] 一致。
type GRPCOptionsContractFunc func(mw *bootstrapv1.Server_Grpc_Middleware) (opts []grpc.ServerOption, close func() error, err error)

// WithGRPCOptionsContract 登记一个按契约 server.grpc.middleware 段构造的 gRPC
// 服务端选项工厂（多次登记按登记顺序累加，与 [WithGRPCOptions] 同队列）。
//
// 与 [WithGRPCOptions] 的差别只有一个入参：本钩子拿到契约段，故
// bundle.FromGRPCMiddleware 翻译出来的层配置真的会进链——这是该段的正式消费口。
// 两条口混用时链序 = 先全部无参工厂、后全部契约工厂（求值时点不同，见
// Decl.GRPCOptionContractFns 注释）；要精确交错，只用一条。
func WithGRPCOptionsContract(fn GRPCOptionsContractFunc) Option {
	return optionFunc(func(s *Decl) {
		if fn != nil {
			s.GRPCOptionContractFns = append(s.GRPCOptionContractFns, fn)
		}
	})
}

// WithGRPC 声明启用 gRPC 服务器并注册业务 service（单值，末者胜）。
//
// 服务端选项（拦截器链、credentials）一律经 [WithGRPCOptions] 登记；TLS 由框架
// 从契约 server.grpc.tls 段构造，不在选项里配。
func WithGRPC(register func(*grpc.Server)) Option {
	return optionFunc(func(s *Decl) { s.GRPCRegister = register })
}

// WithGRPCOptions 登记一个 gRPC 服务端选项工厂，在 Run 期 server 构造点求值。
//
// 用途：选项构造依赖 beforeStart 建立的运行期资源（认证器/授权器/仓储）时，
// 值切片拿到的会是 nil，工厂函数则看到就绪后的依赖。多次登记按登记顺序累加，
// 与 [WithGRPC] 的 unary 同队列。
//
// 典型用法：
//
//	appkit.WithGRPC(registerSvc)                  // 只声明 service 注册
//	appkit.WithGRPCOptions(newInterceptorChain)   // 拦截器链延到 Run 期构造
func WithGRPCOptions(fn func() []grpc.ServerOption) Option {
	return optionFunc(func(s *Decl) {
		if fn != nil {
			s.GRPCOptionFuncs = append(s.GRPCOptionFuncs, fn)
		}
	})
}

// WithExtraServerFunc 注册**运行期构造**的附加服务器。
//
// 与 Servers 的差别：后者收已构造实例（调用点即需存在）；本 Option 收
// 工厂函数，在 Run 期 beforeStart 链末尾（业务钩子之后）调用——故可消费业务建立
// 的运行期资源（如就绪的认证器/授权器）。这是应用层「构造期占位」的替代品。
//
// 构造出的 server 并入停机序列，由 appkit 统一 Stop（无需业务登记 Effect）。
// 工厂返回 nil 表示本次不装配（降级，如依赖未配置）；返回错误则启动失败并回滚。
//
// 本钩子**独立于** http/grpc 是否声明——只声明额外服务器（无主协议面）时同样生效。
func WithExtraServerFunc(fn func(context.Context) (transport.Server, error)) Option {
	return optionFunc(func(s *Decl) { s.ServersFunc = append(s.ServersFunc, fn) })
}

// loggerSource 统一「默认两阶段日志工厂」与「logger Provider 接管」的调用面：
// 阶段 A（契约未装载）与阶段 B/热更（按契约或候选重建）各一个调用点。
// Provider 路径的 scope.App 恒为 nil——日志装配先于资源容器存在，且热更路径
// 在配置 watch goroutine 上执行，不得触碰 App；Build 须并发安全（阶段 B
// 与热更可能并发进入）。
type loggerSource interface {
	bootstrapLogger(ctx context.Context) (log.Logger, func(), error)
	contractLogger(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (log.Logger, func(), error)
}

// providerLoggerSource 是 logger Provider 路径（唯一路径：slog 后端经
// StandardLoggerProvider 走同一条 provider 通道进 std）：
// 引擎在三个时点调用 Provider.Build（阶段 A：Config=nil——框架在此保证启动
// 日志可见；阶段 B/热更：Config=完整契约或候选），产物为 log.Logger，cleanup
// 交由引擎的 curCleanup 原子换槽兑现。
type providerLoggerSource struct {
	prov Provider
	node Node
	spec *Decl
}

func (s providerLoggerSource) build(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (log.Logger, func(), error) {
	scope := ApplyScope{Config: cfg, Decl: s.spec, Role: s.spec.Role, Node: s.node}
	product, cleanup, err := s.prov.Build(ctx, scope, s.node)
	if err != nil {
		return nil, nil, err
	}
	lg, ok := product.(log.Logger)
	if !ok {
		if c := cleanup; c != nil {
			_ = c(ctx)
		}
		return nil, nil, fmt.Errorf("appkit: logger provider %v returned %T, want log.Logger", s.prov.Segments(), product)
	}
	var fc func()
	if cleanup != nil {
		fc = func() { _ = cleanup(context.Background()) }
	}
	return lg, fc, nil
}

func (s providerLoggerSource) bootstrapLogger(ctx context.Context) (log.Logger, func(), error) {
	return s.build(ctx, nil)
}

func (s providerLoggerSource) contractLogger(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (log.Logger, func(), error) {
	return s.build(ctx, cfg)
}

// resolveLoggerSource 解析日志源：logger 段 Provider 即接管（std 内置与
// 第三方同通道，段路径末者胜）；零声明时 plan 已回退内置
// StandardLoggerProvider——「框架持有一个引导 logger」由其阶段 A 分支
// （Config=nil）兑现，阶段 B 用 Provider 结果替换（loggerSource 两阶段协议）。
func resolveLoggerSource(spec *Decl, plan *providerPlan) loggerSource {
	return providerLoggerSource{prov: plan.loggerProv, node: plan.loggerNode, spec: spec}
}

// driverGRPCGateway 是契约 server.http.driver 的转码模式值。与
// transport/gateway/contract.DriverGRPCGateway 同值——appkit 不 import
// gateway（根模块与适配器模块互 require 即成模块环），对偶值在此独立声明，
// 准入校验由 gateway 的 HTTPProvider 在构造期兑现。
const driverGRPCGateway = "grpc-gateway"
