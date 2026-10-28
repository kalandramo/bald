package appkit

// 约定装配的内部实现（assemble）：由**段驱动 Provider 循环**驱动（机制见
// provider.go）——每个资源域 = 1 个 Provider 声明 + 段存在性由契约反射给出，
// 框架内不再维护任何手工段枚举表。框架自有后端的默认 Provider 集合由
// std.Standard()（pkg/appkit/std）打包注入。
//
// 动机：显式 Option 逐项装配让业务 main.go 手写 Bind×3、BeforeStart 装载+
// 校验+重建 Logger、OnConfigChange 再装载等样板。约定装配把「契约里已有形状
// 的部分」收敛为框架约定，业务只声明配置表达不了的业务能力。
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
// 约定清单（assemble 的行为，经 Serve 触达）：
//   - App 元数据取自契约 app 段（空值回退默认）；
//   - server.http / server.grpc 段非 nil 即 Bind（flag --server.* 可覆盖契约）；
//     注意：契约有段但业务未用 WithHTTP/WithGRPC/WithGatewayRegister 声明能力时，
//     对应 flag 变更不产生效果（没有 server 消费）——能力声明在代码，是刻意的；
//   - Bind --log.* flag 壳：仅注册 flag 定义，终值以契约 logger 段为准；
//   - 启动期先用默认 Logger（阶段 A），BeforeStart 完成装载+校验后按契约
//     logger 段重建（阶段 B），停机经 Effect 恢复原 Logger 并释放后端；
//   - config 段 Provider（构造期）产出配置层（声明序=层优先级，首元素最高），
//     cleanup 挂 Effect 停机逆序释放；
//   - OnConfigChange：热更新副本试装载+校验后整契约原子落盘，并重建 Logger
//     （log.level 即改即生效）；坏配置降级记日志、保留旧契约。

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

// WithHTTP 声明启用 HTTP 服务器，业务 handler 必供（探针路径由框架兜底）。
func WithHTTP(handler http.Handler) Option {
	return optionFunc(func(s *Decl) { s.HTTPHandler = handler })
}

// WithGatewayRegister 声明 grpc-gateway 转码能力：REST → gRPC 转码注册回调，
// 业务自建 runtime.ServeMux、组合 pb.RegisterXxxHandler 后交回 http.Handler。
//
// 网关不是独立服务器，而是 server.http 段的一种模式，由契约 server.http.driver
// 驱动装配（std server Provider 的模式分支）：
//   - driver 为 "grpc-gateway"（或留空）→ HTTP 端口即网关转码面；
//   - driver 为其他值 → 构造期 fail-fast（转码能力无消费面）。
//
// 与 WithHTTP 同时声明时 driver 必须显式为 "grpc-gateway"（显式 > 隐式，
// 留空会让两个能力静默竞争同一端口）；handler 由此被转码面替代，业务要
// 混合路由时在回调里返回组合 handler。
func WithGatewayRegister(fn func(context.Context, *grpc.ClientConn) (http.Handler, error)) Option {
	return optionFunc(func(s *Decl) { s.GatewayRegister = fn })
}

// WithGRPC 声明启用 gRPC 服务器。register 注册业务 service；unary 为完整
// 拦截器链（链序是安全策略，归业务：ErrorInterceptor 须挂最外层）。
func WithGRPC(register func(*grpc.Server), unary ...grpc.ServerOption) Option {
	return optionFunc(func(s *Decl) { s.GRPCRegister, s.GRPCUnary = register, unary })
}

// WithGRPCOptions 注册**运行期求值**的 gRPC 服务器选项。
//
// 与 WithGRPC 的 unary 参数差别：后者是值切片，在**调用点**即求值（Go 变参语义），
// 若选项构造依赖 beforeStart 建立的运行期资源（认证器/授权器/仓储），构造期拿到的
// 是 nil。本 Option 登记一个工厂函数，在 Run 期 server 构造钩子内调用——此时业务
// beforeStart 已跑完，依赖就绪。可与 WithGRPC 的 unary 并用（二者累加，静态在前）。
//
// 典型用法（消除「请求期解析」的认证代理）：
//
//	appkit.WithGRPC(registerSvc)                  // 只声明 service 注册
//	appkit.WithGRPCOptions(newInterceptorChain)   // 拦截器链延到 Run 期构造
func WithGRPCOptions(fn func() []grpc.ServerOption) Option {
	return optionFunc(func(s *Decl) { s.GRPCOptionsFn = fn })
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

// logDecoratorsOpt 附加 bslog 日志装饰器（B7 取消导出：装饰归 provider 自持
// ——装饰器类型是具体日志后端的 bslog.Option，经公开 Option 暴露即把后端类型
// 泄露进声明面）。内置 slog Provider（StandardLoggerProvider）消费 Decl 的
// 装饰器；第三方 logger Provider 自持装饰。
func logDecoratorsOpt(deco ...bslog.Option) Option {
	return optionFunc(func(s *Decl) { s.logDecorators = append(s.logDecorators, deco...) })
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

// validateCapabilities 校验代码侧能力声明与契约段的对偶关系（纯函数，无
// 副作用）：WithHTTP/Gateway 需 server.http 段、WithGRPC 需 server.grpc 段、
// 探针路径不得相同、网关 driver 只能由 grpc-gateway 消费。assemble 与 Compile
// 共用——`--plan` 的预检与真实启动对同一批错误 fail-fast。
func validateCapabilities(spec *Decl, cfg *bootstrapv1.BootstrapConfig) error {
	httpCfg := cfg.GetServer().GetHttp()
	grpcCfg := cfg.GetServer().GetGrpc()
	if spec.HTTPHandler != nil && httpCfg == nil {
		return errors.New("appkit: WithHTTP declared but bootstrap.server.http is nil")
	}
	if spec.GatewayRegister != nil && httpCfg == nil {
		return errors.New("appkit: WithGatewayRegister declared but bootstrap.server.http is nil")
	}
	if spec.GRPCRegister != nil && grpcCfg == nil {
		return errors.New("appkit: WithGRPC declared but bootstrap.server.grpc is nil")
	}
	if spec.Health != nil && spec.ProbeLivePath == spec.ProbeReadyPath {
		// 探针要挂在同一个 mux 上，同路径注册会 panic——启动期 fail-fast 暴露。
		return fmt.Errorf("appkit: WithHealth probe paths must differ, got %q twice", spec.ProbeLivePath)
	}
	if spec.GatewayRegister != nil {
		// 网关面由 server.http.driver 选择：非 grpc-gateway 的显式值 = 转码能力
		// 无消费面；与 WithHTTP 同时声明且留空 = 两个能力静默竞争同一端口。
		// 都属启动期错误，fail-fast 暴露（显式 > 隐式）。
		switch d := httpCfg.GetDriver(); {
		case d != "" && d != baldbootstrap.DriverGrpcGateway:
			return fmt.Errorf("appkit: WithGatewayRegister declared but server.http.driver = %q, want %q or empty", d, baldbootstrap.DriverGrpcGateway)
		case spec.HTTPHandler != nil && d == "":
			return fmt.Errorf("appkit: WithHTTP and WithGatewayRegister are both declared; set server.http.driver = %q to serve the transcoding face", baldbootstrap.DriverGrpcGateway)
		}
	}
	return nil
}

// assemble 按契约约定装配 App——段驱动 Provider 装配引擎
// （公开入口只剩 Serve/MustServe，见 serve.go）。
//
// spec 为业务能力声明（With*/Providers 写入的 Decl，调用方已 Apply 完毕）；
// cfg 为契约，必须非 nil——Serve 路径由内部契约装载产出（bconf.NewBootstrap +
// 预装载），包内测试经显式注入 cfg 复用同一构造期语义。返回的 App 已完成全部约定装配，直接 Run 即可。
func assemble(spec *Decl, cfg *bootstrapv1.BootstrapConfig) (*App, error) {
	if cfg == nil {
		return nil, errors.New("appkit: assemble: bootstrap config is nil")
	}
	// 段驱动装配计划：路径末者胜 → Provider.Plan → 节点冲突裁决 + 统一排序
	//（机制见 provider.go）。计划是执行顺序的唯一来源。
	plan, err := newProviderPlan(context.Background(), spec, cfg)
	if err != nil {
		return nil, err
	}

	// 域声明解析（机制见 domain.go）：装配期校验 fail-fast
	// 先于阶段 A Logger 等一切副作用；Services 并入 gRPC 注册回调（先于下方
	// 能力声明检查——域提供的注册回调与 WithGRPC 声明同权）。
	if err := validateDomains(spec, cfg.GetServer().GetGrpc() != nil); err != nil {
		return nil, err
	}
	composeDomainServices(spec)

	httpCfg := cfg.GetServer().GetHttp()
	grpcCfg := cfg.GetServer().GetGrpc()
	if err := validateCapabilities(spec, cfg); err != nil {
		return nil, err
	}

	// 日志源：logger 段 Provider（零声明回退内置 StandardLoggerProvider，消费
	// Decl 的装饰器）。解析结果不落入 Decl（非用户声明），经参数传递。
	logSrc := resolveLoggerSource(spec, plan)

	// 阶段 A：默认 Logger，保证契约装载前的启动日志可见。失败路径回滚。
	oldLogger := log.GetLogger()
	bootLogger, bootCleanup, err := logSrc.bootstrapLogger(context.Background())
	if err != nil {
		return nil, fmt.Errorf("appkit: bootstrap logger: %w", err)
	}
	// cleanup 归一化：阶段 A 工厂可返回 nil（bslog 无资源），后续错误回滚、
	// rebuildLogger 兑现旧钩子、停机 Effect 释放均直接调用，不逐处判 nil。
	if bootCleanup == nil {
		bootCleanup = func() {}
	}
	log.SetLogger(bootLogger)

	// 计划期诊断（同 Key 覆盖等逃逸舱）在引导 logger 就绪后可见——与 --plan
	// 输出同源（I10）。
	for _, w := range plan.warnings {
		log.Warn(context.Background(), "appkit: provider plan warning",
			"code", w.Code, "subject", w.Subject, "message", w.Message)
	}

	// 构造期阶段：config 段 Provider 产出配置层（声明序=层优先级，首元素
	// 最高）。scope.App 为 nil——App 尚未构造。失败回滚阶段 A。
	var layers []baldconfig.Layer
	var layersCleanup func()
	if len(plan.construct) > 0 {
		built, err := runProviderNodes(context.Background(), plan.construct,
			ApplyScope{Config: cfg, Decl: spec, Role: spec.Role}, nil)
		if err != nil {
			log.SetLogger(oldLogger)
			bootCleanup()
			return nil, fmt.Errorf("appkit: config layers: %w", err)
		}
		for _, r := range built {
			ls, ok := r.product.([]baldconfig.Layer)
			if !ok {
				rollbackBuilds(built, context.Background())
				log.SetLogger(oldLogger)
				bootCleanup()
				return nil, fmt.Errorf("appkit: config provider %v returned %T, want []config.Layer", r.node.Segments, r.product)
			}
			layers = append(layers, ls...)
			if c := noopCleanupFn(r.cleanup); c != nil {
				prev := layersCleanup
				layersCleanup = func() {
					if prev != nil {
						prev()
					}
					c()
				}
			}
		}
	}

	// curCleanup 跟踪当前生效 Logger 的后端释放钩子：阶段 B 与热更新重建时
	// 原子替换（替换前旧钩子被同步兑现——带缓冲后端的尾批不因热切换丢失，
	// 见 rebuildLogger），停机 Effect 里释放最新一个。
	var curCleanup atomic.Pointer[func()]
	curCleanup.Store(&bootCleanup)

	// 停机槽位：阶段 B 各节点的 cleanup 在 Build 成功后按节点 Key 填充；
	// Effect 按 teardownOrder 的逆序注册（I5）——回放序即节点声明的停机序
	//（Apply 逆序 + TeardownAfter），不再有手工 cleanupOrder。
	slots := newCleanupSlots(plan)
	// a 先声明再进闭包：BeforeStart 在 Run 期才执行，届时 a 已赋值。
	var a *App
	// 声明域的可协调节点键（装配末期由 registerDomainReconcilers 填充）：
	// 域 Wire 成功后逐个记为已应用——实际态由 Wire 建立。
	var domainReconcileKeys []ResourceKey

	// 注：配置命名空间的必填检查在 Serve（serve.go）——它在任何副作用（阶段 A
	// Logger、契约预装载）之前 fail-fast，比旧 FromBootstrap 把检查放在阶段 A
	// 之后更早暴露；assemble 不重复检查（测试注入契约的路径同样适用）。

	kitOpts := []Option{
		ID(cfg.GetApp().GetId()),
		Name(orDefault(cfg.GetApp().GetName(), "bald-app")),
		configNamespaceOpt(spec.configNamespace), // B6：Serve 已按契约 app.name 派生
		Version(orDefault(cfg.GetApp().GetVersion(), "v0.0.0")),
		Env(cfg.GetApp().GetEnv()),
		StopTimeout(orDuration(cfg.GetApp().GetStopTimeout().AsDuration(), 30*time.Second)),
		// --log.* flag 壳：仅注册 flag 定义参与合并，终值以契约 logger 段为准
		//（阶段 B 从契约重建 Logger，flag→合并→Unmarshal→契约的链路经此打通）。
		Bind("", baldbootstrap.LogOptions(nil)),
		ConfigLayers(layers...),
		Servers(spec.Servers...),
		BeforeStart(func(ctx context.Context) error {
			// 阶段 B 前置框架步骤：装载配置快照进契约 + 校验 + 按契约重建
			// Logger（必须在校验后重建——构造用的契约值已含文件/env 覆盖；
			// registry 段的注册中心构造已 Provider 化，在下方循环进行）。
			if err := syncBootstrap(a, cfg, spec, logSrc, &curCleanup); err != nil {
				return err
			}
			// 阶段 B Provider 循环：计划序（阶段内 Kahn 拓扑 + (Priority,
			// Key)；机制见 provider.go）。显式 Registrar 实例接管 registry 段
			// 时纯 registry Provider 已在计划期跳过（程序化接管 = 刻意覆盖）。
			_, err := runProviderNodes(ctx, plan.stageB, ApplyScope{Config: cfg, Decl: spec, Role: spec.Role, App: a},
				func(r buildResult) error { return storeProduct(a, r, slots) })
			if err != nil {
				return err
			}
			// L3 段覆盖检查（见 segment_coverage.go）：装配链
			// 末尾、契约已装载定型——枚举实际出现的顶层段，减去本次装配真
			// 实被消费的段与白名单，剩余即启动期 error（script 段零消费者的
			// 静默失效由此拦截）。
			return checkSegmentCoverage(cfg, spec, plan)
		}),
		OnConfigChange(func(m map[string]any) {
			hotReload(a, cfg, spec, logSrc, &curCleanup, m)
		}),
	}
	// 框架停机 Effect：按计划推导的停机序逆序注册（I5）——回放即停机序
	//（audit-restore 由 TeardownAfter ["*"] 排到最后：stream 尾批审计事件在
	// 指标/span flush 之后收集；B8 起无「恢复装配前全局」步骤——全局审计器
	// 已删除）。
	kitOpts = append(kitOpts, slots.effects(oldLogger, &curCleanup)...)
	if httpCfg != nil {
		kitOpts = append(kitOpts, Bind("server.http", httpCfg))
	}
	if grpcCfg != nil {
		kitOpts = append(kitOpts, Bind("server.grpc", grpcCfg))
	}
	if spec.Registrar != nil {
		kitOpts = append(kitOpts, Registrar(spec.Registrar))
	}
	if spec.RemoteConfig != nil {
		kitOpts = append(kitOpts, RemoteConfig(spec.RemoteConfig))
	}
	for _, fn := range spec.AfterStart {
		kitOpts = append(kitOpts, AfterStart(fn))
	}
	if spec.ConfigFile != "" {
		kitOpts = append(kitOpts, ConfigFile(spec.ConfigFile))
	}
	if spec.WatchConfigFile {
		kitOpts = append(kitOpts, WatchConfigFile(true))
	}
	if layersCleanup != nil {
		kitOpts = append(kitOpts, effectOpt("appkit:config-layers", func(context.Context) error {
			layersCleanup()
			return nil
		}))
	}

	// 业务透传：钩子/协调器/能力声明/订阅/效果——追加在框架装配之后
	//（beforeStart 在内部装载链后执行，可读契约终值；Effect 逆序回放先于
	// 框架 Effect——业务资源先收）。声明经 mech.* 等导出构造面写入 Decl，
	// 引擎经未导出 *Opt 写入面透传（B5 后导出构造面归 mech）。
	for _, fn := range spec.BeforeStart {
		kitOpts = append(kitOpts, BeforeStart(fn))
	}
	// 域装配钩子（机制见 domain.go）：注册序在业务钩子之后
	//（域可消费业务钩子建立的运行期资源）、迟后阶段之前（域的路由/服务注册
	// 先于 server 构建生效）——「资源面已就绪」与「注册先于 server 构建」两
	// 约束夹出的窗口。多域次序 = 声明序；域 Wire/Routes 失败 fail-fast。
	if len(spec.Domains) > 0 {
		kitOpts = append(kitOpts, BeforeStart(func(ctx context.Context) error {
			if err := wireDomains(ctx, a, spec); err != nil {
				return err
			}
			// Wire 成功 = 声明的域资源已建到期望态：可协调节点落账，Drift
			// 不把正常启动误报为 Missing（热更路径由 applyPlanChange 落账）。
			for _, key := range domainReconcileKeys {
				a.markAppliedByKey(key)
			}
			return nil
		}))
	}
	// 迟后阶段（server 段 Provider）：**追加在业务 beforeStart 之后**——注册序
	// 即执行序，故本钩子在业务钩子后执行，此时业务已建成运行期资源（DB/
	// 仓储/biz/handler），register 回调（gRPC service 注册）与 HTTP handler
	// 消费它们即拿到真实依赖。构造失败的错误经 Run 的 beforeStart 失败路径
	// 回滚（Effect 逆序回放配置层与阶段 A Logger），与旧实现等价。
	if len(plan.late) > 0 {
		kitOpts = append(kitOpts, BeforeStart(func(ctx context.Context) error {
			_, err := runProviderNodes(ctx, plan.late, ApplyScope{Config: cfg, Decl: spec, Role: spec.Role, App: a},
				func(r buildResult) error { return storeServerProduct(a, r) })
			return err
		}))
	}
	// 运行期额外服务器（WithExtraServerFunc）：同样追加在业务 beforeStart 之后
	//（注册序=执行序），故工厂可读业务建立的运行期资源。**独立于** server 段
	// Provider 是否存在——只声明额外服务器（无主协议面）时本钩子仍生效。
	if len(spec.ServersFunc) > 0 {
		fns := spec.ServersFunc
		kitOpts = append(kitOpts, BeforeStart(func(ctx context.Context) error {
			for _, fn := range fns {
				srv, err := fn(ctx)
				if err != nil {
					return fmt.Errorf("appkit: build extra server: %w", err)
				}
				if srv != nil {
					a.servers = append(a.servers, srv)
				}
			}
			return nil
		}))
	}
	for _, fn := range spec.BeforeStop {
		kitOpts = append(kitOpts, BeforeStop(fn))
	}
	for _, e := range spec.Effects {
		kitOpts = append(kitOpts, effectOpt(e.Name, e.Undo))
	}
	for _, r := range spec.Reconciles {
		kitOpts = append(kitOpts, reconcileOpt(r.Name, r.Fn))
	}
	for _, k := range spec.KeyWatches {
		kitOpts = append(kitOpts, onKeyChangeOpt(k.Key, k.Fn))
	}
	if len(spec.Provides) > 0 {
		kitOpts = append(kitOpts, providesOpt(spec.Provides...))
	}
	for _, r := range spec.Requires {
		kitOpts = append(kitOpts, requiresOpt(r.Component, r.Caps...))
	}
	if len(spec.Components) > 0 {
		kitOpts = append(kitOpts, componentsOpt(spec.Components...))
	}
	// 业务配置 flag 绑定：与框架自身的 server.http/server.grpc 绑定同层，
	// 使业务 flag 进入装载 FlagSet 的 flag 层（最高优先级）。
	for _, b := range spec.Binds {
		kitOpts = append(kitOpts, Bind(b.Prefix, b.Opt))
	}
	// 严格契约校验（opt-in，见 StrictContract）：业务段白名单原样透传。
	if spec.StrictContract {
		kitOpts = append(kitOpts, StrictContract(spec.BusinessSections...))
	}

	a = NewApp(kitOpts...) // BeforeStart 闭包引用 a，执行时已赋值（Run 期才回调）

	// M3 计划快照 + 节点协调器：计划是配置变更 diff 的基线；可协调的 Provider
	// 节点（audit 等）在配置变更时按同一 Provider 重建产物并替换 cleanup 槽位。
	fullPlan, err := planFrom(context.Background(), spec, cfg, plan)
	if err != nil {
		return nil, err
	}
	a.installPlan(fullPlan)
	for _, e := range plan.stageB {
		if e.node.Reload != ReloadReconcile {
			continue
		}
		prov, node := e.prov, e.node
		a.nodeReconcilers[node.Key] = func(ctx context.Context, _ *Plan) error {
			product, cleanup, err := prov.Build(ctx, ApplyScope{Config: cfg, Decl: spec, Role: spec.Role, App: a}, node)
			if err != nil {
				return fmt.Errorf("appkit: reconcile provider %v: %w", node.Segments, err)
			}
			return storeProductReplacing(ctx, a, buildResult{prov: prov, node: node, product: product, cleanup: cleanup}, slots)
		}
	}
	// 声明域的可协调节点：协调钩子归域自持（Domain.Reconcile），与 Provider
	// 节点共用同一 NodeReconciler 面——配置变更按同一 Key diff 分发。
	domainReconcileKeys = registerDomainReconcilers(a, plan, fullPlan)
	return a, nil
}

// registerDomainReconcilers 为声明域的 ReloadReconcile 节点注册协调适配器：
// 把 NodeReconciler(ctx, next) 转成 Domain.Reconcile(ctx, app, node)——node 取
// next 里该 Key 的期望节点（缺省回退装配期节点）。返回待落账的节点键，供
// 域 Wire 成功后记为已应用。
func registerDomainReconcilers(a *App, plan *providerPlan, fullPlan *Plan) []ResourceKey {
	keys := make([]ResourceKey, 0, len(plan.domainNodes))
	for _, n := range fullPlan.Nodes {
		dom, ok := plan.domainNodes[n.Key]
		if !ok || n.Reload != ReloadReconcile || dom.Reconcile == nil {
			continue
		}
		key, hook, want := n.Key, dom.Reconcile, n
		a.nodeReconcilers[key] = func(ctx context.Context, next *Plan) error {
			node := want
			if cur := lookupPlanNode(next, key); cur != nil {
				node = *cur
			}
			return hook(ctx, a, node)
		}
		keys = append(keys, n.Key)
	}
	return keys
}

// lookupPlanNode 按 Key 取计划节点（nil 计划返回 nil）。
func lookupPlanNode(p *Plan, key ResourceKey) *Node {
	if p == nil {
		return nil
	}
	for i := range p.Nodes {
		if p.Nodes[i].Key == key {
			return &p.Nodes[i]
		}
	}
	return nil
}

// cleanupSlots 是停机清理槽位集：节点 Key → cleanup（Build 成功后填充）。
// 停机序在计划期已由 teardownOrder 推出（I5），Effect 按该序逆序注册。
type cleanupSlots struct {
	cleanups  map[ResourceKey]*Cleanup
	order     []ResourceKey          // 停机序（先执行者在前）
	name      map[ResourceKey]string // 节点 Effect 名
	loggerKey ResourceKey
}

func newCleanupSlots(plan *providerPlan) *cleanupSlots {
	s := &cleanupSlots{
		cleanups:  make(map[ResourceKey]*Cleanup, len(plan.teardown)),
		order:     plan.teardown,
		name:      plan.effectName,
		loggerKey: plan.loggerNode.Key,
	}
	for _, key := range plan.teardown {
		c := Cleanup(nil)
		s.cleanups[key] = &c
	}
	return s
}

// replace 以新 cleanup 替换节点槽位并同步兑现旧钩子（配置协调路径；
// 返回旧钩子的错误，调用方决定是否仅记日志）。
func (s *cleanupSlots) replace(ctx context.Context, key ResourceKey, c Cleanup) {
	slot := s.cleanups[key]
	if slot == nil {
		return
	}
	old := *slot
	*slot = c
	if old != nil {
		if err := old(ctx); err != nil {
			log.Error(ctx, "appkit node cleanup failed during reconcile", "node", string(key), "error", err)
		}
	}
}

// fill 填充节点槽位（同 Key 只 Build 一次；聚合分支保留给防御性重复填充，
// 后填者先执行）。
func (s *cleanupSlots) fill(key ResourceKey, c Cleanup) {
	slot := s.cleanups[key]
	if slot == nil || c == nil {
		return
	}
	if *slot == nil {
		*slot = c
		return
	}
	prev := *slot
	*slot = func(ctx context.Context) error {
		if err := c(ctx); err != nil {
			return err
		}
		return prev(ctx)
	}
}

// effects 生成停机 Effect：按停机序逆序注册，回放即停机序（I5）。logger
// 槽位特供 bootstrap-logger——恢复装配前全局 Logger 并兑现最新后端 cleanup
// （热更重建原子换槽的钩子在此收口）。
func (s *cleanupSlots) effects(oldLogger log.Logger, curCleanup *atomic.Pointer[func()]) []Option {
	opts := make([]Option, 0, len(s.order))
	for i := len(s.order) - 1; i >= 0; i-- {
		key := s.order[i]
		name := s.name[key]
		if key == s.loggerKey {
			opts = append(opts, effectOpt(name, func(context.Context) error {
				log.SetLogger(oldLogger)
				if c := curCleanup.Load(); c != nil {
					(*c)()
				}
				return nil
			}))
			continue
		}
		slot := s.cleanups[key]
		opts = append(opts, effectOpt(name, func(ctx context.Context) error {
			if c := *slot; c != nil {
				return c(ctx)
			}
			return nil
		}))
	}
	return opts
}

// noopCleanupFn 把 Cleanup 转成无参聚合用闭包（config 层 cleanup 聚合）。
func noopCleanupFn(c Cleanup) func() {
	if c == nil {
		return nil
	}
	return func() { _ = c(context.Background()) }
}

// storeProduct 把阶段 B Provider 的产物按顶层段落进 App 的资源面，并把
// cleanup 填入对应槽位。产物形状不符 = 装配错误（fail-fast）。
func storeProduct(a *App, r buildResult, slots *cleanupSlots) error {
	return storeProductWith(a, r, func(key ResourceKey, c Cleanup) { slots.fill(key, c) })
}

// storeProductReplacing 是配置协调路径的落账：新产物替换旧产物，旧 cleanup
// 在替换后同步兑现（连接池/暴露端不因热替换泄漏）。
func storeProductReplacing(ctx context.Context, a *App, r buildResult, slots *cleanupSlots) error {
	return storeProductWith(a, r, func(key ResourceKey, c Cleanup) { slots.replace(ctx, key, c) })
}

func storeProductWith(a *App, r buildResult, put func(ResourceKey, Cleanup)) error {
	top := segmentTop(r.node.Segments[0])
	switch top {
	case "registry":
		reg, ok := r.product.(registry.Registrar)
		if !ok {
			return fmt.Errorf("appkit: registry provider %v returned %T, want registry.Registrar", r.node.Segments, r.product)
		}
		a.registrar = reg
		a.setResources(top, reg)
		put(r.node.Key, r.cleanup)
	case "database", "cache", "storage", "ai", "workflow", "broker":
		m, err := productStringMap(r)
		if err != nil {
			return err
		}
		a.setResources(top, m)
		put(r.node.Key, r.cleanup)
	case "audit":
		aud, ok := r.product.(audit.Auditor)
		if !ok {
			return fmt.Errorf("appkit: audit provider %v returned %T, want audit.Auditor", r.node.Segments, r.product)
		}
		// B8：全局 SetAuditor 已删除——契约产物作 appkit 自身审计事件的兜底
		// sink（显式 Auditor Option 优先，不覆盖；先前契约产物可被协调替换），
		// 并照常落资源面供业务显式注入 bundle/中间件。
		if a.auditor == nil || a.auditorFromContract {
			a.auditor = aud
			a.auditorFromContract = true
		}
		a.setResources(top, aud)
		put(r.node.Key, r.cleanup)
	case "tracer":
		// 产物不落资源面（tracer shutdown 由 Provider 自持，cleanup 槽位承担
		// 停机回收）。
		put(r.node.Key, r.cleanup)
	case "metrics":
		// 产物为可关闭的暴露端（B7：MetricsProvider 返回 MetricsExposure 而非
		// 裸 Handler，监听面归 metricshttp 能力包）。停机序「先停暴露端再
		// flush」由引擎在此合成进 cleanup 槽位——Provider 只需返回 (端点, flush)。
		var exp MetricsExposure
		if r.product != nil {
			e, ok := r.product.(MetricsExposure)
			if !ok {
				return fmt.Errorf("appkit: metrics provider %v returned %T, want MetricsExposure (nil = no exposure)", r.node.Segments, r.product)
			}
			exp = e
		}
		flush := r.cleanup
		var slot Cleanup
		if exp != nil || flush != nil {
			slot = func(ctx context.Context) error {
				if exp != nil {
					if err := exp.Stop(ctx); err != nil {
						log.Error(ctx, "appkit metrics exposure stop failed", "error", err.Error())
					}
				}
				if flush != nil {
					return flush(ctx)
				}
				return nil
			}
		}
		put(r.node.Key, slot)
	default:
		// 表外段：cleanup 照常入账，产物按统一约定落资源面（B4 收口）：
		// map 产物按 "<顶层段>.<子段>" 展开、非 map 产物落 "<顶层段>"。
		a.setResources(top, r.product)
		put(r.node.Key, r.cleanup)
	}
	a.markAppliedByKey(r.node.Key)
	return nil
}

// setResources 把段产物落进统一资源面（B4）：map[string]any 产物按
// "<top>.<子段>" 逐键展开（与六域产物的段路径约定同构，nil 值跳过），
// 其余产物整体落 "<top>"。仅 BeforeStart（阶段 B）单线程写入，Run 后只读。
func (a *App) setResources(top string, product any) {
	if product == nil {
		return
	}
	if m, ok := product.(map[string]any); ok {
		for child, v := range m {
			if v == nil {
				continue
			}
			a.resources[top+"."+child] = v
		}
		return
	}
	a.resources[top] = product
}

// productStringMap 归一六资源域产物：map[string]any（键=子段名；nil 视为空）。
func productStringMap(r buildResult) (map[string]any, error) {
	if r.product == nil {
		return map[string]any{}, nil
	}
	m, ok := r.product.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("appkit: provider %v returned %T, want map[string]any", r.node.Segments, r.product)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// storeServerProduct 落迟后阶段（server 段）产物：[]transport.Server 追加进
// 服务器列表，cleanup 经 addEffect 入账（构造即入账，停机先 drain 服务器）。
func storeServerProduct(a *App, r buildResult) error {
	var servers []transport.Server
	switch v := r.product.(type) {
	case nil:
	case []transport.Server:
		servers = v
	case transport.Server:
		servers = []transport.Server{v}
	default:
		return fmt.Errorf("appkit: server provider %v returned %T, want []transport.Server", r.node.Segments, r.product)
	}
	a.servers = append(a.servers, servers...)
	if r.cleanup != nil {
		c := r.cleanup
		a.addEffect("appkit:servers", func(ctx context.Context) error { return c(ctx) })
	}
	return nil
}

// syncBootstrap 装载配置快照进契约、校验并重建 Logger（阶段 B 前置框架步骤
// ——必须在校验后重建，构造用的契约值已含文件/env 覆盖；registry 段的注册
// 中心构造已 Provider 化，在阶段 B 循环按 canonical 序进行）。
func syncBootstrap(a *App, cfg *bootstrapv1.BootstrapConfig, spec *Decl, logSrc loggerSource, curCleanup *atomic.Pointer[func()]) error {
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
	// 以合并后的契约（文件/env/flag/层）重算并安装计划：配置变更 diff 的
	// 基线必须是真实生效的契约，而非装配前的预装载形态。
	next, err := compilePlan(context.Background(), spec, cfg)
	if err != nil {
		return err
	}
	a.installPlan(next)
	if err := rebuildLogger(cfg, logSrc, curCleanup); err != nil {
		return err
	}
	for _, n := range next.Nodes {
		if n.Kind == "logger" {
			a.markAppliedByKey(n.Key)
			break
		}
	}
	return nil
}

// hotReload 热更新：默认值试装载 + 校验 + 计划重算，通过后整契约原子落盘、
// 重建 Logger 并按计划差异协调节点（M3）。任一步失败都降级记日志、保留旧
// 契约，不阻断热更新流程（与审计旁路同哲学）。
// Unmarshal 只做结构转换、不校验取值，坏值必须由 Validate 拦在落盘之前。
//
// candidate 从「默认值 + 全量设置快照」重建，而不是克隆当前契约再合并：
// settings 是 Store 的完整合并结果（等价于冷启动的输入），而 proto3 标量无
// presence，合并进旧契约时零值（false/0/""）无法覆盖旧的非零值——热更把
// 开关从 true 改回 false 会静默失效。从默认值重建则天然是「按新快照求值」。
func hotReload(a *App, cfg *bootstrapv1.BootstrapConfig, spec *Decl, logSrc loggerSource, curCleanup *atomic.Pointer[func()], m map[string]any) {
	const msg = "appkit: config hot-reload"
	ctx := context.Background()
	lg := log.GetLogger()

	candidate := bconf.NewBootstrap()
	if err := baldconfig.Unmarshal(m, candidate); err != nil {
		lg.Error(ctx, msg, "err", err)
		return
	}
	if err := bconf.Validate(candidate); err != nil {
		lg.Error(ctx, msg, "err", err)
		return
	}
	// 计划重算在落盘前完成：新契约若无法编译（段覆盖/能力冲突等），整次
	// 热更新被拒绝，旧契约与旧计划保持一致（不做半生效）。
	next, err := compilePlan(ctx, spec, candidate)
	if err != nil {
		lg.Error(ctx, msg, "err", err)
		return
	}
	// 原子落盘：整体替换 cfg 内容。禁止 `*cfg = *candidate`——proto 消息
	// 内嵌 MessageState（含 sync.Mutex），按值赋值会复制锁（vet copylocks）
	// 且破坏其 lazy 状态。Reset+Merge 是 protobuf 规范的整体替换写法；
	// candidate 为本函数私有副本（落盘后即弃），深拷贝成本可忽略。
	cfg.Reset()
	proto.Merge(cfg, candidate)

	// logger 走专用两阶段/热更协议：重建成功才落账已应用摘要，失败由 Drift
	// 如实报 Stale。
	loggerOK := rebuildLogger(cfg, logSrc, curCleanup) == nil
	if !loggerOK {
		lg.Error(ctx, msg, "err", errors.New("logger rebuild failed; drift will report stale"))
	}
	a.applyPlanChange(ctx, next, m)
	if loggerOK {
		for _, n := range next.Nodes {
			if n.Kind == "logger" {
				a.markAppliedByKey(n.Key)
				break
			}
		}
	}
}

// rebuildLogger 按契约 logger 段重建全局 Logger，并原子替换释放钩子。
//
// 换后端时旧 cleanup 被同步兑现（先切全局句柄、再冲刷旧后端）：带缓冲的
// 后端（loki batchSize 未满的尾批）在热更新切换时不丢日志。旧 cleanup 若
// 阻塞（远端不可达）由后端自身的 flush 超时上界保护。
func rebuildLogger(cfg *bootstrapv1.BootstrapConfig, logSrc loggerSource, curCleanup *atomic.Pointer[func()]) error {
	lg, cleanup, err := logSrc.contractLogger(context.Background(), cfg)
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
