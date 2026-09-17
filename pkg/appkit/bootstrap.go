package appkit

// FromBootstrap：契约约定装配入口（2026-09-06 新增，纯增量，不改动 New/Option 语义）。
//
// 动机：appkit.New 是显式 Option 装配，业务 main.go 需要手写 Bind×3、
// BeforeStart 装载+校验+重建 Logger、OnConfigChange 再装载等样板。
// FromBootstrap 把「契约里已有形状的部分」收敛为框架约定，main.go 只声明
// 配置表达不了的业务能力。
//
// 分工边界（配置驱动参数，代码声明能力）：
//
//	配置驱动（契约 / flag / env / 配置文件）：app 元数据（id/name/version/
//	env/stop_timeout）、server 地址与 TLS、日志级别/格式/输出、配置源层、
//	注册中心实例、热更新开关。
//	代码声明（配置文件表达不了）：业务 handler、gRPC service 注册、
//	拦截器链序（安全策略，如 ErrorInterceptor 必须最外层）、就绪探针的
//	下游依赖、日志装饰器（脱敏等）。
//
// 约定清单：
//   - App 元数据取自契约 app 段（空值回退默认）；
//   - server.http / server.grpc 段非 nil 即 Bind（flag --server.* 可覆盖契约）；
//     注意：契约有段但业务未用 WithHTTP/WithGRPC/WithGatewayRegister 声明能力时，
//     对应 flag 变更不产生效果（没有 server 消费）——能力声明在代码，是刻意的；
//   - Bind --log.* flag 壳：仅注册 flag 定义，终值以契约 logger 段为准；
//   - 启动期先用默认 Logger（阶段 A），BeforeStart 完成装载+校验后按契约
//     logger 段重建（阶段 B），停机经 Effect 恢复原 Logger 并释放后端；
//   - WithConfigRegistry 时经 bootstrap.Registry.Build 产出配置层
//     （注册序=层优先级，首元素最高），cleanup 挂 Effect 停机逆序释放；
//   - OnConfigChange：热更新副本试装载+校验后整契约原子落盘，并重建 Logger
//     （log.level 即改即生效）；坏配置降级记日志、保留旧契约。
//
// 与 e2e 复用机制（newApp + Option 覆盖）不冲突：FromBootstrap 是另一条
// 构造路径，两者可并存。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
	baldconfig "github.com/kalandramo/bald/bootstrap/config"
	log "github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/log/bslog"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/registry"
	"github.com/kalandramo/bald/transport"
)

// loggerFactory 是解析后的日志后端工厂（私有类型：不进入公开 API，
// 由 resolveLoggerFactory 产出、FromBootstrap 装载链与 rebuildLogger 消费）。
type loggerFactory = func(ctx context.Context, l *bootstrapv1.Logger) (lg log.Logger, cleanup func(), err error)

// bootstrapSpec 汇集 FromBootstrap 的业务能力声明。
type bootstrapSpec struct {
	httpHandler     http.Handler
	gatewayRegister func(context.Context, *grpc.ClientConn) (http.Handler, error)
	grpcRegister    func(*grpc.Server)
	grpcUnary       []grpc.ServerOption
	readiness       transport.ReadinessFunc
	registrar       registry.Registrar
	regRegistry     *RegistrarRegistry

	configFile  string
	watchFiles  bool
	cfgRegistry *baldbootstrap.Registry
	remote      baldconfig.RemoteSource

	extraServers []transport.Server
	afterStart   []func(context.Context) error

	logDeco     []bslog.Option
	logRegistry *baldbootstrap.LogRegistry
	logFac      loggerFactory // resolveLoggerFactory 的解析结果（非用户 Option）。

	dbRegistry       *baldbootstrap.DatabaseRegistry
	cacheRegistry    *baldbootstrap.CacheRegistry
	storageRegistry  *baldbootstrap.StorageRegistry
	aiRegistry       *baldbootstrap.AiRegistry
	workflowRegistry *baldbootstrap.WorkflowRegistry
	brokerRegistry   *baldbootstrap.BrokerRegistry

	tracerRegistry  *TracerRegistry
	metricsRegistry *MetricsRegistry
	auditRegistry   *AuditRegistry

	// 业务透传声明（U1：重业务走 FromBootstrap 的逃生面——钩子/协调器/
	// 能力声明/订阅/效果账本/组件，转发给 New 的同名 Option）。
	beforeStart []func(context.Context) error
	beforeStop  []func(context.Context) error
	effects     []effectDecl
	reconciles  []reconcileDecl
	keyWatches  []keyWatchDecl
	provides    []string
	requires    []requireDecl
	components  []Component
}

// effectDecl / reconcileDecl / keyWatchDecl / requireDecl 是透传声明的
// 载体（与 New 的 Option 参数一一对应，仅用于 spec 暂存）。
type effectDecl struct {
	name string
	undo func(context.Context) error
}

type reconcileDecl struct {
	name string
	fn   ReconcileFunc
}

type keyWatchDecl struct {
	key string
	fn  func(old, new string)
}

type requireDecl struct {
	component string
	caps      []string
}

// BootstrapOption 声明 FromBootstrap 的业务能力（与 New 的 Option 分属两个
// 类型，避免混淆：BootstrapOption 在构造前生效，Option 语义由 FromBootstrap 内化）。
type BootstrapOption func(*bootstrapSpec)

// WithHTTP 声明启用 HTTP 服务器，业务 handler 必供（探针路径由框架兜底）。
func WithHTTP(handler http.Handler) BootstrapOption {
	return func(s *bootstrapSpec) { s.httpHandler = handler }
}

// WithGatewayRegister 声明 grpc-gateway 转码能力：REST → gRPC 转码注册回调，
// 业务自建 runtime.ServeMux、组合 pb.RegisterXxxHandler 后交回 http.Handler。
//
// 网关不是独立服务器，而是 server.http 段的一种模式，由契约 server.http.driver
// 驱动装配（HttpServerProvider 的模式分支）：
//   - driver 为 "grpc-gateway"（或留空）→ HTTP 端口即网关转码面；
//   - driver 为其他值 → 构造期 fail-fast（转码能力无消费面）。
//
// 与 WithHTTP 同时声明时 driver 必须显式为 "grpc-gateway"（显式 > 隐式，
// 留空会让两个能力静默竞争同一端口）；handler 由此被转码面替代，业务要
// 混合路由时在回调里返回组合 handler。
func WithGatewayRegister(fn func(context.Context, *grpc.ClientConn) (http.Handler, error)) BootstrapOption {
	return func(s *bootstrapSpec) { s.gatewayRegister = fn }
}

// WithGRPC 声明启用 gRPC 服务器。register 注册业务 service；unary 为完整
// 拦截器链（链序是安全策略，归业务：ErrorInterceptor 须挂最外层）。
func WithGRPC(register func(*grpc.Server), unary ...grpc.ServerOption) BootstrapOption {
	return func(s *bootstrapSpec) { s.grpcRegister, s.grpcUnary = register, unary }
}

// WithReadiness 注入共享就绪探针（HTTP /readyz 与 gRPC health 同源）。
// 缺省时探针恒就绪（/readyz 200、health SERVING）。
func WithReadiness(fn transport.ReadinessFunc) BootstrapOption {
	return func(s *bootstrapSpec) { s.readiness = fn }
}

// WithRegistrar 注入注册中心实例（如 inmemory.New()）。
// 显式实例优先于契约装配（阶段 B 的 buildRegistrar 检测到非 nil 即跳过）。
func WithRegistrar(r registry.Registrar) BootstrapOption {
	return func(s *bootstrapSpec) { s.registrar = r }
}

// WithRegistrarRegistry 注入服务注册中心的契约装配注册表（显式注册
// provider，见 RegistrarRegistry）。契约 registry 段存在且未显式
// WithRegistrar 时，阶段 B 按 registry.type 构造注册中心。
func WithRegistrarRegistry(rr *RegistrarRegistry) BootstrapOption {
	return func(s *bootstrapSpec) { s.regRegistry = rr }
}

// WithRemoteConfig 接入远程配置源（config.RemoteSource，如 kratos 桥接的 etcd/nacos）。
// 远程作基准，契约层/本地文件覆盖之（优先级见 Store 合并约定）。
func WithRemoteConfig(src baldconfig.RemoteSource) BootstrapOption {
	return func(s *bootstrapSpec) { s.remote = src }
}

// WithExtraServers 追加附加服务器（契约形状表达不了的服务器，如独立端口的
// 自定义监听面），与主服务器并行启停。grpc-gateway 转码面不走此逃生舱，
// 用 WithGatewayRegister + 契约 server.http.driver 声明。
func WithExtraServers(servers ...transport.Server) BootstrapOption {
	return func(s *bootstrapSpec) { s.extraServers = append(s.extraServers, servers...) }
}

// WithAfterStart 注册启动完成回调（服务已监听、注册已完成，可打 endpoint 日志/预热）。
func WithAfterStart(fn func(context.Context) error) BootstrapOption {
	return func(s *bootstrapSpec) { s.afterStart = append(s.afterStart, fn) }
}

// WithConfigFile 声明本地配置文件（语义同 appkit.ConfigFile，单路径）。
// 已传 WithConfigRegistry 且其中含 file 源时无需重复声明。
func WithConfigFile(path string) BootstrapOption {
	return func(s *bootstrapSpec) { s.configFile = path }
}

// WithWatchConfig 启用本地配置文件热更新（fsnotify）。
func WithWatchConfig(watch bool) BootstrapOption {
	return func(s *bootstrapSpec) { s.watchFiles = watch }
}

// WithConfigRegistry 注入契约 Config 段的源装配注册表（显式注册 provider，
// 注册序即层优先级）。FromBootstrap 负责 Build 与 cleanup 停机释放。
func WithConfigRegistry(r *baldbootstrap.Registry) BootstrapOption {
	return func(s *bootstrapSpec) { s.cfgRegistry = r }
}

// WithLogDecorators 附加 bslog 日志装饰器（脱敏、业务字段等），阶段 A/B
// 构造 Logger 时统一生效。注意：装饰器是 bslog.Option，仅对默认路径的
// slog 项与阶段 A 回退生效，其余后端（loki/aliyun 等）不消费装饰器。
func WithLogDecorators(deco ...bslog.Option) BootstrapOption {
	return func(s *bootstrapSpec) { s.logDeco = append(s.logDeco, deco...) }
}

// WithLogRegistry 声明契约驱动的日志后端注册表：按契约 logger.backends
// 逐项查表构造后端（查表 / 多项合并 / 出口脱敏 / 未注册 fail-fast 均由
// bootstrap.LogRegistry 全权负责）。
//
// 不声明本 Option 时默认路径为纯函数分派（无注册表、依赖增量为零）：
// 仅支持 slog 项（+业务装饰器）与阶段 A 回退，其余项 fail-fast
// 并给出本 Option 的用法示例。远端/自定义后端必须经本 Option 注册：
//
//	reg := baldbootstrap.NewLogRegistry()
//	reg.MustRegister(lokicontract.Type, lokicontract.Provider) // log/loki/contract
//	reg.MustRegister("slog", baldbootstrap.BslogLoggerProvider()) // 可选；不注册则 slog 项 fail-fast
//	appkit.FromBootstrap(cfg, appkit.WithLogRegistry(reg))
//
// 两阶段语义：阶段 A（契约装载前）回退默认 bslog 保证启动日志可见；
// 阶段 B 按 logger.backends 逐项查表重建，未注册的项构造期 fail-fast。
// WithLogDecorators 仅对默认路径的 slog 项与阶段 A 回退生效；注册表
// 路径要消费装饰器，注册一个 deco-aware 的 slog provider 即可。
func WithLogRegistry(r *baldbootstrap.LogRegistry) BootstrapOption {
	return func(s *bootstrapSpec) { s.logRegistry = r }
}

// WithTracerRegistry 注入 trace 后端的契约装配注册表（显式注册 provider，
// 见 TracerRegistry）。契约 tracer 段存在时，阶段 B 按 tracer.type 构造
// 全局 TracerProvider，shutdown 挂停机 Effect 最后回放（收集尾批 span）。
// tracer 段不支持热更新。
func WithTracerRegistry(r *TracerRegistry) BootstrapOption {
	return func(s *bootstrapSpec) { s.tracerRegistry = r }
}

// WithMetricsRegistry 注入指标后端的契约装配注册表（显式注册 provider，
// 见 MetricsRegistry）。契约 metrics 段存在时，阶段 B 按 metrics.type 装配
// 双通道（prometheus 抓取端独立端口 + 可选 OTLP 直推），flush 与暴露端
// 关闭挂停机 Effect。metrics 段不支持热更新。
func WithMetricsRegistry(r *MetricsRegistry) BootstrapOption {
	return func(s *bootstrapSpec) { s.metricsRegistry = r }
}

// WithAuditRegistry 注入审计后端的契约装配注册表（显式注册 provider，
// 见 AuditRegistry）。契约 audit 段存在时，阶段 B 按 audit.backends
// 列表装配（log 内置无需注册；store/stream 走 contract 注册；多后端
// 组装 MultiAuditor）并 SetAuditor 全局注入，停机 Effect 恢复装配前
// 全局（T1 纪律）+ 聚合 cleanup（stream flush 尾批）。audit 段不支持
// 热更新（运行期热切走 R1-2 协调器）。
func WithAuditRegistry(r *AuditRegistry) BootstrapOption {
	return func(s *bootstrapSpec) { s.auditRegistry = r }
}

// ---------------------------------------------------------------------------
// 业务透传（U1）：FromBootstrap 表达不了、但重业务需要的生命周期面——转发
// 给 New 的同名 Option。语义与直用 New 完全一致，仅注册顺序有约定：
//   - WithBeforeStart 追加在 FromBootstrap 内部装载链之后（契约装载/校验/
//     Logger 重建/各 Registry Build 完成后执行——业务钩子里可直接读终值）；
//   - WithEffect 注册在框架 Effect 之后——停机逆序回放时业务效果先执行
//     （业务资源先收，框架底座后收）。
// ---------------------------------------------------------------------------

// WithBeforeStart 追加业务启动钩子：在 FromBootstrap 内部的装载链（配置装载
// →校验→Logger 重建→registrar/可观测性/数据客户端装配）之后执行。业务在此
// 读契约终值装配桥接（DB 连接、Authn/Authz 桥等）。
func WithBeforeStart(fn func(context.Context) error) BootstrapOption {
	return func(s *bootstrapSpec) { s.beforeStart = append(s.beforeStart, fn) }
}

// WithBeforeStop 追加业务停机前钩子（与 New 的 BeforeStop 语义一致）。
func WithBeforeStop(fn func(context.Context) error) BootstrapOption {
	return func(s *bootstrapSpec) { s.beforeStop = append(s.beforeStop, fn) }
}

// WithEffect 声明业务停机效果（逆序回放；与 New 的 Effect 语义一致，见上
// 方顺序约定）。
func WithEffect(name string, undo func(ctx context.Context) error) BootstrapOption {
	return func(s *bootstrapSpec) { s.effects = append(s.effects, effectDecl{name: name, undo: undo}) }
}

// WithReconcile 声明期望态协调器（与 New 的 Reconcile 语义一致：首次收敛在
// 配置基线装载后、AfterStart 前触发，其后每次配置变更再次触发）。
func WithReconcile(name string, fn ReconcileFunc) BootstrapOption {
	return func(s *bootstrapSpec) { s.reconciles = append(s.reconciles, reconcileDecl{name: name, fn: fn}) }
}

// WithOnKeyChange 订阅单个配置 key 的变更（与 New 的 OnKeyChange 语义一致，
// key 级增量协调）。
func WithOnKeyChange(key string, fn func(old, new string)) BootstrapOption {
	return func(s *bootstrapSpec) { s.keyWatches = append(s.keyWatches, keyWatchDecl{key: key, fn: fn}) }
}

// WithProvides 声明本进程提供的能力（S1 启动期 fail-fast，与 New 的 Provides
// 语义一致）。
func WithProvides(caps ...string) BootstrapOption {
	return func(s *bootstrapSpec) { s.provides = append(s.provides, caps...) }
}

// WithRequires 声明组件依赖的能力（与 New 的 Requires 语义一致）。
func WithRequires(component string, caps ...string) BootstrapOption {
	return func(s *bootstrapSpec) { s.requires = append(s.requires, requireDecl{component: component, caps: caps}) }
}

// WithComponents 声明进程内组件（C1 生命周期：Start 于服务器监听前、
// Dispose 于停机末段逆序，与 New 的 Components 语义一致）。
func WithComponents(comps ...Component) BootstrapOption {
	return func(s *bootstrapSpec) { s.components = append(s.components, comps...) }
}

// FromBootstrap 按契约约定装配 AppKit。
//
// cfg 为契约（通常 bconf.NewBootstrap() 后由业务填充或经 flag 覆盖），
// 必须非 nil。返回的 AppKit 已完成全部约定装配，直接 Run 即可。
func FromBootstrap(cfg *bootstrapv1.BootstrapConfig, opts ...BootstrapOption) (*AppKit, error) {
	if cfg == nil {
		return nil, errors.New("appkit: FromBootstrap: bootstrap config is nil")
	}
	spec := &bootstrapSpec{}
	for _, o := range opts {
		if o != nil {
			o(spec)
		}
	}
	// 日志后端工厂两级分发：契约驱动注册表（WithLogRegistry：阶段 A 无
	// 契约回退默认 bslog，阶段 B 按 logger.backends 逐项查表构造）；
	// 不声明时默认纯函数路径（仅 slog 项 + 教学报错，依赖增量为零）。
	spec.logFac = resolveLoggerFactory(spec)

	httpCfg := cfg.GetServer().GetHttp()
	grpcCfg := cfg.GetServer().GetGrpc()
	if spec.httpHandler != nil && httpCfg == nil {
		return nil, errors.New("appkit: WithHTTP declared but bootstrap.server.http is nil")
	}
	if spec.gatewayRegister != nil && httpCfg == nil {
		return nil, errors.New("appkit: WithGatewayRegister declared but bootstrap.server.http is nil")
	}
	if spec.grpcRegister != nil && grpcCfg == nil {
		return nil, errors.New("appkit: WithGRPC declared but bootstrap.server.grpc is nil")
	}
	if spec.gatewayRegister != nil {
		// 网关面由 server.http.driver 选择：非 grpc-gateway 的显式值 = 转码能力
		// 无消费面；与 WithHTTP 同时声明且留空 = 两个能力静默竞争同一端口。
		// 都属启动期错误，fail-fast 暴露（显式 > 隐式）。
		switch d := httpCfg.GetDriver(); {
		case d != "" && d != baldbootstrap.DriverGrpcGateway:
			return nil, fmt.Errorf("appkit: WithGatewayRegister declared but server.http.driver = %q, want %q or empty", d, baldbootstrap.DriverGrpcGateway)
		case spec.httpHandler != nil && d == "":
			return nil, fmt.Errorf("appkit: WithHTTP and WithGatewayRegister are both declared; set server.http.driver = %q to serve the transcoding face", baldbootstrap.DriverGrpcGateway)
		}
	}

	// 阶段 A：默认 Logger，保证契约装载前的启动日志可见。失败路径回滚。
	oldLogger := log.GetLogger()
	bootLogger, bootCleanup, err := spec.logFac(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("appkit: bootstrap logger: %w", err)
	}
	// cleanup 归一化：阶段 A 工厂可返回 nil（bslog 无资源），后续错误回滚、
	// rebuildLogger 兑现旧钩子、停机 Effect 释放均直接调用，不逐处判 nil。
	if bootCleanup == nil {
		bootCleanup = func() {}
	}
	log.SetLogger(bootLogger)

	// 契约 Config 段 → 配置层（注册序=层优先级）。失败回滚阶段 A。
	var layers []baldconfig.Layer
	var layersCleanup func()
	if spec.cfgRegistry != nil {
		layers, layersCleanup, err = spec.cfgRegistry.Build(context.Background(), cfg)
		if err != nil {
			log.SetLogger(oldLogger)
			bootCleanup()
			return nil, fmt.Errorf("appkit: config layers: %w", err)
		}
	}

	// 服务器构造走 bootstrap.ServerRegistry（契约驱动装配层，与直用 bootstrap
	// 同一实现，消除双真相源）：能力声明 → provider 注入，契约段 → 装配。
	// 未声明的能力不注册 provider——契约段存在也无 server 消费（能力声明在
	// 代码，是刻意的）；零能力声明（纯后台进程）跳过构造，servers 为空合法。
	// BuildServers 失败回滚阶段 A 与配置层，与 ConfigRegistry 错误路径对称。
	var servers []transport.Server
	var serversCleanup func()
	if spec.httpHandler != nil || spec.gatewayRegister != nil || spec.grpcRegister != nil {
		sr := baldbootstrap.NewServerRegistry()
		if spec.httpHandler != nil || spec.gatewayRegister != nil {
			httpOpts := []baldbootstrap.HTTPServerOption{
				baldbootstrap.WithHTTPHandler(spec.httpHandler),
				baldbootstrap.WithHTTPReadiness(spec.readiness),
			}
			if spec.gatewayRegister != nil {
				httpOpts = append(httpOpts, baldbootstrap.WithGatewayRegister(spec.gatewayRegister))
			}
			sr.MustRegister("http", baldbootstrap.HttpServerProvider(httpOpts...))
		}
		if spec.grpcRegister != nil {
			sr.MustRegister("grpc", baldbootstrap.GrpcServerProvider(
				baldbootstrap.WithGRPCUnary(spec.grpcUnary...),
				baldbootstrap.WithGRPCRegister(spec.grpcRegister),
				baldbootstrap.WithGRPCReadiness(spec.readiness),
			))
		}
		built, cleanup, err := sr.BuildServers(context.Background(), cfg.GetServer())
		if err != nil {
			log.SetLogger(oldLogger)
			bootCleanup()
			if layersCleanup != nil {
				layersCleanup()
			}
			return nil, fmt.Errorf("appkit: build servers: %w", err)
		}
		servers = append(servers, built...)
		serversCleanup = cleanup
	}
	servers = append(servers, spec.extraServers...)

	// curCleanup 跟踪当前生效 Logger 的后端释放钩子：阶段 B 与热更新重建时
	// 原子替换（替换前旧钩子被同步兑现——带缓冲后端的尾批不因热切换丢失，
	// 见 rebuildLogger），停机 Effect 里释放最新一个。
	var curCleanup atomic.Pointer[func()]
	curCleanup.Store(&bootCleanup)

	// regCleanup 跟踪契约装配注册中心的 client 释放钩子（阶段 B 构造时填入）。
	// Deregister 先于 stopAll 执行，Effect 回放在 stopAll 内——顺序安全。
	var regCleanup atomic.Pointer[func()]

	// dbCleanup 跟踪契约装配数据库客户端的连接池释放钩子（阶段 B 构建时
	// 填入）。Effect 注册在 servers 之前——停机逆序回放保证服务器先 drain、
	// 数据库连接最后关。
	var dbCleanup func()
	// cacheCleanup 同理跟踪缓存实例释放钩子；Effect 注册在 database-clients
	// 之后——逆序回放保证缓存先关（加速层，关了不影响正确性）、数据库最后关。
	var cacheCleanup func()
	// storageCleanup 跟踪对象存储客户端释放钩子；Effect 注册在 cache-clients
	// 之后——停机逆序：storage 先关（暂无长连接）、缓存次之、数据库最后关。
	var storageCleanup func()
	// aiCleanup 跟踪 AI 客户端释放钩子；Effect 注册在 storage-clients 之后
	// （当前三后端均无连接池，cleanup 为 nil，Effect 预留）。
	var aiCleanup func()
	// workflowCleanup/brokerCleanup 跟踪工作流客户端与消息代理断连钩子；
	// Effect 注册在 ai-clients 之后——停机逆序：broker（in-flight 消息）→
	// workflow → ai → storage → cache → database。
	var workflowCleanup func()
	var brokerCleanup func()
	// obs 跟踪阶段 B 装配的可观测性句柄（tracer shutdown / metrics 暴露端 +
	// flush）。Effect 注册在 bootstrap-logger 之前——停机逆序回放最后执行：
	// 所有组件 cleanup 完成、Logger 恢复后，再停暴露端、flush 尾批指标与 span。
	var obs *observabilityState
	// auditSt 跟踪阶段 B 装配的审计后端句柄（装配前全局 prev + 后端 cleanup）。
	// Effect 注册在 tracer-shutdown 之前——逆序回放最后执行：stream 尾批审计
	// 事件在指标/span flush 之后收集（收集面最全），再恢复装配前全局 Auditor。
	var auditSt *auditState

	// a 先声明再进闭包：BeforeStart 在 Run 期才执行，届时 a 已赋值。
	var a *AppKit
	kitOpts := []Option{
		ID(cfg.GetApp().GetId()),
		Name(orDefault(cfg.GetApp().GetName(), "bald-app")),
		Version(orDefault(cfg.GetApp().GetVersion(), "v0.0.0")),
		Env(cfg.GetApp().GetEnv()),
		StopTimeout(orDuration(cfg.GetApp().GetStopTimeout().AsDuration(), 30*time.Second)),
		// --log.* flag 壳：仅注册 flag 定义参与合并，终值以契约 logger 段为准
		//（阶段 B 从契约重建 Logger，flag→合并→Unmarshal→契约的链路经此打通）。
		Bind("", baldbootstrap.LogOptions(nil)),
		ConfigLayers(layers...),
		Servers(servers...),
		BeforeStart(func(context.Context) error {
			if err := syncBootstrap(a, cfg, spec, &curCleanup, &regCleanup); err != nil {
				return err
			}
			// 可观测性（tracer/metrics 段）在注册中心之后、数据/缓存客户端
			// 之前装配：使 Recorder / tracer 在首个请求与后续组件初始化 span
			// 前接入全局 Provider（`=` 赋值外层，T7 教训：闭包内 `:=` 遮蔽
			// 致停机 Effect 拿 nil）。
			o, err := buildObservability(cfg, spec)
			if err != nil {
				return err
			}
			obs = o
			// 审计后端（audit 段）在可观测性之后装配：SetAuditor 全局注入，
			// 使首个请求前中间件/appkit 的审计事件即进契约后端（`=` 赋值外层，
			// T7 教训同上）。
			au, err := buildAudit(cfg, spec)
			if err != nil {
				return err
			}
			auditSt = au
			// 数据库/缓存客户端构建在注册中心之后：任一步失败走 Run 失败路径
			// 回滚 Effect 账本（各 Effect 自行释放）。赋值外层变量（勿用 :=，
			// 否则遮蔽导致 Effect 回放拿到 nil）。
			cleanup, err := buildDatabases(a, cfg, spec)
			if err != nil {
				return err
			}
			dbCleanup = cleanup
			if cleanup, err = buildCaches(a, cfg, spec); err != nil {
				return err
			}
			cacheCleanup = cleanup
			if cleanup, err = buildStorages(a, cfg, spec); err != nil {
				return err
			}
			storageCleanup = cleanup
			if cleanup, err = buildAis(a, cfg, spec); err != nil {
				return err
			}
			aiCleanup = cleanup
			if cleanup, err = buildWorkflows(a, cfg, spec); err != nil {
				return err
			}
			workflowCleanup = cleanup
			if cleanup, err = buildBrokers(a, cfg, spec); err != nil {
				return err
			}
			brokerCleanup = cleanup
			return nil
		}),
		OnConfigChange(func(m map[string]any) {
			hotReload(cfg, spec, &curCleanup, m)
		}),
		// 审计 Effect 注册在 tracer-shutdown 之前：逆序回放时最后执行——
		// 先 cleanup（stream flush 尾批审计事件，在指标/span 之后收集面最全），
		// 再恢复装配前的全局 Auditor（T1：全局写入配套逆操作）。
		Effect("appkit:audit-restore", func(ctx context.Context) error {
			if auditSt == nil {
				return nil
			}
			if auditSt.cleanup != nil {
				if err := auditSt.cleanup(ctx); err != nil {
					log.Error(ctx, "appkit audit cleanup failed", "error", err.Error())
				}
			}
			audit.SetAuditor(auditSt.prev)
			return nil
		}),
		// 可观测性 Effect 注册在 bootstrap-logger 之前：逆序回放时最后执行
		// （所有组件 cleanup、Logger 恢复之后）——先停 metrics 暴露端 +
		// flush 指标，再 flush trace（收集全部尾批 span）。
		Effect("appkit:tracer-shutdown", func(ctx context.Context) error {
			if obs == nil || obs.traceShutdown == nil {
				return nil
			}
			return obs.traceShutdown(ctx)
		}),
		Effect("appkit:metrics-server", func(ctx context.Context) error {
			if obs == nil {
				return nil
			}
			if obs.metricsSrv != nil {
				if err := obs.metricsSrv.Shutdown(ctx); err != nil {
					log.Error(ctx, "appkit metrics server shutdown failed", "error", err.Error())
				}
			}
			if obs.metricsFlush != nil {
				return obs.metricsFlush(ctx)
			}
			return nil
		}),
		Effect("appkit:bootstrap-logger", func(context.Context) error {
			log.SetLogger(oldLogger)
			if c := curCleanup.Load(); c != nil {
				(*c)()
			}
			return nil
		}),
		Effect("appkit:registrar-client", func(context.Context) error {
			if c := regCleanup.Load(); c != nil {
				(*c)()
			}
			return nil
		}),
		Effect("appkit:database-clients", func(context.Context) error {
			if dbCleanup != nil {
				dbCleanup()
			}
			return nil
		}),
		Effect("appkit:cache-clients", func(context.Context) error {
			if cacheCleanup != nil {
				cacheCleanup()
			}
			return nil
		}),
		Effect("appkit:storage-clients", func(context.Context) error {
			if storageCleanup != nil {
				storageCleanup()
			}
			return nil
		}),
		Effect("appkit:ai-clients", func(context.Context) error {
			if aiCleanup != nil {
				aiCleanup()
			}
			return nil
		}),
		Effect("appkit:workflow-clients", func(context.Context) error {
			if workflowCleanup != nil {
				workflowCleanup()
			}
			return nil
		}),
		Effect("appkit:broker-clients", func(context.Context) error {
			if brokerCleanup != nil {
				brokerCleanup()
			}
			return nil
		}),
	}
	if httpCfg != nil {
		kitOpts = append(kitOpts, Bind("server.http", httpCfg))
	}
	if grpcCfg != nil {
		kitOpts = append(kitOpts, Bind("server.grpc", grpcCfg))
	}
	if spec.registrar != nil {
		kitOpts = append(kitOpts, Registrar(spec.registrar))
	}
	if spec.remote != nil {
		kitOpts = append(kitOpts, RemoteConfig(spec.remote))
	}
	for _, fn := range spec.afterStart {
		kitOpts = append(kitOpts, AfterStart(fn))
	}
	if spec.configFile != "" {
		kitOpts = append(kitOpts, ConfigFile(spec.configFile))
	}
	if spec.watchFiles {
		kitOpts = append(kitOpts, WatchConfigFile(true))
	}
	if layersCleanup != nil {
		kitOpts = append(kitOpts, Effect("appkit:config-layers", func(context.Context) error {
			layersCleanup()
			return nil
		}))
	}
	if serversCleanup != nil {
		kitOpts = append(kitOpts, Effect("appkit:servers", func(context.Context) error {
			serversCleanup()
			return nil
		}))
	}

	// 业务透传（U1）：钩子/协调器/能力声明/订阅/效果——追加在框架装配之后
	//（beforeStart 在内部装载链后执行，可读契约终值；Effect 逆序回放先于
	// 框架 Effect——业务资源先收）。
	for _, fn := range spec.beforeStart {
		kitOpts = append(kitOpts, BeforeStart(fn))
	}
	for _, fn := range spec.beforeStop {
		kitOpts = append(kitOpts, BeforeStop(fn))
	}
	for _, e := range spec.effects {
		kitOpts = append(kitOpts, Effect(e.name, e.undo))
	}
	for _, r := range spec.reconciles {
		kitOpts = append(kitOpts, Reconcile(r.name, r.fn))
	}
	for _, k := range spec.keyWatches {
		kitOpts = append(kitOpts, OnKeyChange(k.key, k.fn))
	}
	if len(spec.provides) > 0 {
		kitOpts = append(kitOpts, Provides(spec.provides...))
	}
	for _, r := range spec.requires {
		kitOpts = append(kitOpts, Requires(r.component, r.caps...))
	}
	if len(spec.components) > 0 {
		kitOpts = append(kitOpts, Components(spec.components...))
	}

	a = New(kitOpts...) // BeforeStart 闭包引用 a，执行时已赋值（Run 期才回调）
	return a, nil
}

// syncBootstrap 装载配置快照进契约，重建 Logger，并按契约 registry 段
// 构造注册中心（BeforeStart 阶段 B——必须在校验后执行，构造用的契约值
// 已含文件/env 覆盖）。
func syncBootstrap(a *AppKit, cfg *bootstrapv1.BootstrapConfig, spec *bootstrapSpec, curCleanup, regCleanup *atomic.Pointer[func()]) error {
	m := a.Settings()
	if m == nil {
		return errors.New("appkit: settings snapshot is nil")
	}
	if err := baldconfig.Unmarshal(m, cfg); err != nil {
		return fmt.Errorf("appkit: unmarshal bootstrap config: %w", err)
	}
	if err := bconf.Validate(cfg); err != nil {
		return fmt.Errorf("appkit: validate bootstrap config: %w", err)
	}
	if err := rebuildLogger(cfg.GetLogger(), spec, curCleanup); err != nil {
		return err
	}
	return buildRegistrar(a, cfg, spec, regCleanup)
}

// buildRegistrar 在阶段 B（契约装载校验后）按契约 registry 段构造注册中心。
// 显式 WithRegistrar 优先（实例非 nil 即跳过）；契约段缺失为 no-op；
// 段存在但未接 RegistrarRegistry 则 fail-fast（配置意图无法兑现）。
// registry 段不支持热更新（client 重建侵入性大，变更需重启生效）。
func buildRegistrar(a *AppKit, cfg *bootstrapv1.BootstrapConfig, spec *bootstrapSpec, regCleanup *atomic.Pointer[func()]) error {
	if a.registrar != nil {
		return nil // 显式注入优先
	}
	rcfg := cfg.GetRegistry()
	if rcfg == nil {
		return nil // 未声明注册中心
	}
	if spec.regRegistry == nil {
		return errors.New("appkit: bootstrap.registry present but no RegistrarRegistry wired (WithRegistrarRegistry missing)")
	}
	reg, cleanup, err := spec.regRegistry.Build(context.Background(), rcfg)
	if err != nil {
		return fmt.Errorf("appkit: build registrar: %w", err)
	}
	if cleanup == nil {
		cleanup = func() {}
	}
	a.registrar = reg
	regCleanup.Store(&cleanup)
	return nil
}

// hotReload 热更新：副本试装载 + 校验，通过后整契约原子落盘并重建 Logger。
// 任一步失败都降级记日志、保留旧契约，不阻断热更新流程（与审计旁路同哲学）。
// Unmarshal 只做结构转换、不校验取值，坏值必须由 Validate 拦在落盘之前。
func hotReload(cfg *bootstrapv1.BootstrapConfig, spec *bootstrapSpec, curCleanup *atomic.Pointer[func()], m map[string]any) {
	const msg = "appkit: config hot-reload"
	lg := log.GetLogger()

	candidate, ok := proto.Clone(cfg).(*bootstrapv1.BootstrapConfig)
	if !ok {
		lg.Error(context.Background(), msg, "err", errors.New("proto clone produced unexpected type"))
		return
	}
	if err := baldconfig.Unmarshal(m, candidate); err != nil {
		lg.Error(context.Background(), msg, "err", err)
		return
	}
	if err := bconf.Validate(candidate); err != nil {
		lg.Error(context.Background(), msg, "err", err)
		return
	}
	// 原子落盘：整体替换 cfg 内容。禁止 `*cfg = *candidate`——proto 消息
	// 内嵌 MessageState（含 sync.Mutex），按值赋值会复制锁（vet copylocks）
	// 且破坏其 lazy 状态。Reset+Merge 是 protobuf 规范的整体替换写法；
	// candidate 为本函数私有副本（落盘后即弃），深拷贝成本可忽略。
	cfg.Reset()
	proto.Merge(cfg, candidate)

	if err := rebuildLogger(cfg.GetLogger(), spec, curCleanup); err != nil {
		lg.Error(context.Background(), msg, "err", err)
	}
}

// rebuildLogger 按契约 logger 段重建全局 Logger，并原子替换释放钩子。
//
// 换后端时旧 cleanup 被同步兑现（先切全局句柄、再冲刷旧后端）：带缓冲的
// 后端（loki batchSize 未满的尾批）在热更新切换时不丢日志。旧 cleanup 若
// 阻塞（远端不可达）由后端自身的 flush 超时上界保护。
func rebuildLogger(l *bootstrapv1.Logger, spec *bootstrapSpec, curCleanup *atomic.Pointer[func()]) error {
	lg, cleanup, err := spec.logFac(context.Background(), l)
	if err != nil {
		return fmt.Errorf("appkit: build logger: %w", err)
	}
	if cleanup == nil {
		cleanup = func() {}
	}
	// 先切换全局句柄：新日志直接进新后端，旧后端缓冲就此封闭。
	log.SetLogger(lg)
	// 再兑现旧后端 cleanup（阶段 A 钩子为 noop，首次调用无害）。
	if c := curCleanup.Load(); c != nil {
		(*c)()
	}
	curCleanup.Store(&cleanup)
	return nil
}

// resolveLoggerFactory 按 spec 解析日志后端工厂（两级分发）：
// 契约驱动 LogRegistry（WithLogRegistry）> 默认纯函数路径。
// 两条路径均在阶段 A（cfg=nil，契约装载前）回退默认 bslog，阶段 B 分派。
func resolveLoggerFactory(s *bootstrapSpec) loggerFactory {
	if s.logRegistry != nil {
		reg, deco := s.logRegistry, s.logDeco
		return func(ctx context.Context, l *bootstrapv1.Logger) (log.Logger, func(), error) {
			if l == nil {
				// 阶段 A（契约装载前）：回退默认 bslog，保证启动日志可见。
				lg, c := bslog.NewWithCleanup(baldbootstrap.LogOptions(nil), deco...)
				return lg, c, nil
			}
			return reg.BuildLogger(ctx, l)
		}
	}
	return defaultLoggerFactory(s.logDeco)
}

// defaultLoggerFactory 返回默认路径日志工厂（零 Option 时生效）：
// 无注册表的纯函数分派，依赖增量严格为零（bslog 本就是 bootstrap 直接依赖）。
//
// 三分派：
//   - 阶段 A（l==nil，契约装载前）：回退默认 bslog，保证启动日志可见；
//   - backends 项（多后端广播）：type=slog 项直构（走 deco 特例——deco 是
//     bslog.Option，仅 bslog 后端可消费，保留 WithLogDecorators 在默认路径
//     的既有语义）后 MultiLogger 合并；非 slog 项 fail-fast 教学报错；
//   - backends 为空：fail-fast（契约至少声明一项）。
//
// 全局脱敏（filter_keys 非空）：出口包 log.NewFilterLogger——与注册表路径
// （BuildLogger 出口包装）语义一致。
func defaultLoggerFactory(deco []bslog.Option) loggerFactory {
	return func(_ context.Context, l *bootstrapv1.Logger) (log.Logger, func(), error) {
		if l == nil {
			// 阶段 A（契约装载前）：回退默认 bslog，保证启动日志可见。
			return bslog.New(baldbootstrap.LogOptions(nil), deco...), nil, nil
		}
		bs := l.GetBackends()
		if len(bs) == 0 {
			return nil, nil, fmt.Errorf("appkit: logger.backends is empty (declare at least one backend, e.g. type \"slog\")")
		}
		loggers := make([]log.Logger, 0, len(bs))
		var cleanups []func()
		for i, b := range bs {
			if b.GetType() == "slog" {
				lg, c := bslog.NewWithCleanup(baldbootstrap.LogOptions(b), deco...)
				loggers = append(loggers, lg)
				cleanups = append(cleanups, c)
				continue
			}
			return nil, nil, fmt.Errorf("appkit: logger.backends[%d] type %q: default path supports only \"slog\";\n"+
				"  remote/custom backends need explicit registration:\n"+
				"    reg := bootstrap.NewLogRegistry()\n"+
				"    reg.MustRegister(lokicontract.Type, lokicontract.Provider) // log/loki/contract\n"+
				"    appkit.FromBootstrap(cfg, appkit.WithLogRegistry(reg))", i, b.GetType())
		}
		// 单项直通：不包 MultiLogger 广播层（与 BuildLogger 出口原则对齐，
		// 空脱敏清单下整个出口零多余包装）；多项经 MultiLogger 合并。
		var out log.Logger = loggers[0]
		if len(loggers) > 1 {
			out = log.NewMultiLogger(loggers...)
		}
		// cleanup 按构造序正放释放各 slog 项的文件/轮转句柄（lumberjack 缓冲
		// 冲刷落盘、热更新重建不泄漏——与 BuildLogger 出口对齐，恒非 nil）。
		merged := func() {
			for _, c := range cleanups {
				c()
			}
		}
		return log.NewFilterLogger(out, l.GetFilterKeys()...), merged, nil
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func orDuration(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}
