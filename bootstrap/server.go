package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
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
// 返回值语义与 LoggerProvider 一致：出错短路；nil server 视为无法处理该配置
// （服务器是必需品，BuildServers 会报错而非跳过）；cleanup 释放资源，可为 nil。
//
// 与 go-wind 的差异：业务依赖（拦截器链、service 注册器、健康检查数据源）
// 经 Provider 工厂的 Option 显式注入，不用包级全局变量 + Bootstrap 前副作用调用。
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

// Has 报告名字对应的协议服务器工厂是否已注册。
//
// 供装配层（appkit）做「补注册」前的存在性判断：用户经 WithServerRegistry 注入
// 自己的注册表后，框架仍要保证 http/grpc 两个内置协议可用，但不能覆盖用户已
// 注册的同名 provider（[Register] 对重名 fail-fast）。先 Has 再 MustRegister
// 即可共存。
func (r *ServerRegistry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.providers[name]
	return ok
}

// serverSection 是契约 Server 段的枚举项（proto 字段序即装配序）。
//
// implemented 表示「本仓有该协议的服务端实现、且 appkit 会为其注册 provider」。
// implemented=false 的段在 bconf 契约里有声明（超集契约：先声明后实现），但框架
// 不提供其服务端装配。配了这些段必须 fail-fast——否则它们根本不进入 provider
// 遍历，静默失效，用户以为协议面已监听、实际什么都没发生。此约定与
// [BrokerRegistry.Build] 的 brokerSections 一致（见 broker.go 同位置注释）。
type serverSection struct {
	name        string
	implemented bool
	exists      func(*bootstrapv1.Server) bool
}

// serverSections 覆盖 Server message 的全部 32 个 optional 段。
// 顺序即 proto 字段序（确定性错误信息与遍历序）。
var serverSections = []serverSection{
	{"http", true, func(s *bootstrapv1.Server) bool { return s.GetHttp() != nil }},
	{"grpc", true, func(s *bootstrapv1.Server) bool { return s.GetGrpc() != nil }},

	{"http3", false, func(s *bootstrapv1.Server) bool { return s.GetHttp3() != nil }},
	{"graphql", false, func(s *bootstrapv1.Server) bool { return s.GetGraphql() != nil }},
	{"sse", false, func(s *bootstrapv1.Server) bool { return s.GetSse() != nil }},
	{"websocket", false, func(s *bootstrapv1.Server) bool { return s.GetWebsocket() != nil }},
	{"tcp", false, func(s *bootstrapv1.Server) bool { return s.GetTcp() != nil }},
	{"udp", false, func(s *bootstrapv1.Server) bool { return s.GetUdp() != nil }},
	{"kcp", false, func(s *bootstrapv1.Server) bool { return s.GetKcp() != nil }},
	{"thrift", false, func(s *bootstrapv1.Server) bool { return s.GetThrift() != nil }},
	{"trpc", false, func(s *bootstrapv1.Server) bool { return s.GetTrpc() != nil }},
	{"webtransport", false, func(s *bootstrapv1.Server) bool { return s.GetWebtransport() != nil }},
	{"cron", false, func(s *bootstrapv1.Server) bool { return s.GetCron() != nil }},
	{"hptimer", false, func(s *bootstrapv1.Server) bool { return s.GetHptimer() != nil }},
	{"mcp", false, func(s *bootstrapv1.Server) bool { return s.GetMcp() != nil }},
	{"signalr", false, func(s *bootstrapv1.Server) bool { return s.GetSignalr() != nil }},
	{"socketio", false, func(s *bootstrapv1.Server) bool { return s.GetSocketio() != nil }},
	{"webrtc", false, func(s *bootstrapv1.Server) bool { return s.GetWebrtc() != nil }},
	{"asynq", false, func(s *bootstrapv1.Server) bool { return s.GetAsynq() != nil }},
	{"machinery", false, func(s *bootstrapv1.Server) bool { return s.GetMachinery() != nil }},

	// 以下 11 段是 MQ 服务端，其装配走 broker 契约段（BrokerRegistry），
	// server 侧不提供 provider——配在 server 下同样 fail-fast 并指向 broker。
	{"kafka", false, func(s *bootstrapv1.Server) bool { return s.GetKafka() != nil }},
	{"rabbitmq", false, func(s *bootstrapv1.Server) bool { return s.GetRabbitmq() != nil }},
	{"redis_server", false, func(s *bootstrapv1.Server) bool { return s.GetRedisServer() != nil }},
	{"nats", false, func(s *bootstrapv1.Server) bool { return s.GetNats() != nil }},
	{"mqtt", false, func(s *bootstrapv1.Server) bool { return s.GetMqtt() != nil }},
	{"pulsar", false, func(s *bootstrapv1.Server) bool { return s.GetPulsar() != nil }},
	{"activemq", false, func(s *bootstrapv1.Server) bool { return s.GetActivemq() != nil }},
	{"azuresb", false, func(s *bootstrapv1.Server) bool { return s.GetAzuresb() != nil }},
	{"nsq", false, func(s *bootstrapv1.Server) bool { return s.GetNsq() != nil }},
	{"rocketmq", false, func(s *bootstrapv1.Server) bool { return s.GetRocketmq() != nil }},
	{"sqs", false, func(s *bootstrapv1.Server) bool { return s.GetSqs() != nil }},

	// gateway：grpc-gateway REST 转码面（独立端口）。框架不内置其 provider
	// （需 grpc-gateway 重依赖，不进核心依赖图）——配该段须经 WithServerRegistry
	// 注册 provider（如 bald-admin 经 transport/gateway 桥接），否则 fail-fast。
	{"gateway", false, func(s *bootstrapv1.Server) bool { return s.GetGateway() != nil }},
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
	registered := make(map[string]bool, len(r.providers))
	for k := range r.providers {
		names = append(names, k)
		registered[k] = true
	}
	providers := make(map[string]ServerProvider, len(r.providers))
	for k, v := range r.providers {
		providers[k] = v
	}
	r.mu.RUnlock()
	sort.Strings(names) // 确定性顺序（错误信息与遍历序稳定）

	// 前置校验：契约里配了但「本仓无实现、且无注册 provider」的段必须 fail-fast。
	//
	// 为什么不能只靠下面的 provider 遍历兜底：BuildServers 按**已注册 provider**
	// 枚举，而契约段可能没有任何对应 provider 注册——此时该段根本不进入遍历，
	// 既不报错也不打日志，进程照常启动却不监听该协议（「配了没效果」的静默失效）。
	// 故在此按**契约段**补齐校验，与 BrokerRegistry.Build 的语义对齐。
	//
	// registered 传入已注册段名：使后端子模块提供的实现（如 transport/asynq/
	// contract 注册的 "asynq"）被承认——段名与 provider 名一一对应。
	if err := validateServerSections(cfg, registered); err != nil {
		return nil, nil, err
	}

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

// validateServerSections 校验契约中已配置的 Server 段是否有实现。
// 返回聚合错误（一次列全全部问题，免去逐条往返）。
//
// 判据是「**本仓内置实现**（implemented）**或** 该段已有注册 provider
// （registered）」：
//   - 内置 implemented=true 的段（http/grpc）永远放行；
//   - implemented=false 的段（cron/asynq/…）默认 fail-fast——它们在 bconf
//     定义里存在，但框架本身不提供装配；
//   - 但若装配层已为该段注册了 provider（如业务经 WithServerRegistry +
//     transport/asynq/contract 注入），则视为「有实现」，放行——这正是
//     「协议服务器的实现可来自后端子模块」的语义：段名与 provider 名一一对应。
//
// **仍不**因「段配了但 provider 未注册」而 fail-fast：那是刻意的能力声明机制
// （appkit 未声明 http/grpc 能力时不注册对应 provider，契约段存在也不消费），
// 若在此对未注册 provider 报错，会误伤「只关心 storage/workflow、不装配任何
// 服务器」的合法用例。
//
// registered 为 nil 时退化为「只按内置 implemented 判据」——保持既有调用方
// （无注册表上下文）的行为不变。
func validateServerSections(cfg *bootstrapv1.Server, registered map[string]bool) error {
	var problems []string
	for _, sec := range serverSections {
		if !sec.exists(cfg) {
			continue // 段未配置，跳过
		}
		if sec.implemented || registered[sec.name] {
			continue // 内置实现，或已有外部注册的 provider
		}
		problems = append(problems, fmt.Sprintf(
			"server.%s is configured but has no implementation in this repo "+
				"(remove the section, register a provider for it, or use appkit.WithExtraServers to mount it manually)", sec.name))
	}
	if len(problems) > 0 {
		return fmt.Errorf("bootstrap: invalid server config: %s", strings.Join(problems, "; "))
	}
	return nil
}

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
// 装配层，见《Bald 健康检查装配设计》。
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
// 契约段缺失时返回 nil server（BuildServers 跳过）。
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

// HTTPServerOption 配置 HTTPServerProvider / GatewayServerProvider 的业务依赖。
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
// 契约段缺失时返回 nil server（BuildServers 跳过）。
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
