package appkit

// 约定装配的内部实现（assemble）：原 FromBootstrap（2026-09-06 新增的契约
// 约定装配入口）在批 2 W2a 收敛为 Serve 的内部步骤；批 2 W2b 起，原 12 段
// 手工装配流替换为**段驱动 Provider 循环**（装配层评估 §4.2 候选 A，机制见
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

// providerLoggerSource 是 logger Provider 路径（唯一路径——B7 删除默认分派，
// D1 §4.5：slog 后端经 StandardLoggerProvider 走同一条 provider 通道进 std）：
// 引擎在三个时点调用 Provider.Build（阶段 A：Config=nil——框架在此保证启动
// 日志可见；阶段 B/热更：Config=完整契约或候选），产物为 log.Logger，cleanup
// 交由引擎的 curCleanup 原子换槽兑现。
type providerLoggerSource struct {
	prov Provider
	spec *Decl
}

func (s providerLoggerSource) build(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (log.Logger, func(), error) {
	product, cleanup, err := s.prov.Build(ctx, BuildScope{Config: cfg, Decl: s.spec})
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

// resolveLoggerSource 解析日志源：声明了 logger 段 Provider 即接管（std 内置
// 与第三方同通道，段路径末者胜）；零声明时回退内置 StandardLoggerProvider
// ——「框架持有一个引导 logger」由其阶段 A 分支（Config=nil）兑现，阶段 B 用
// Provider 结果替换（loggerSource 两阶段协议沿用）。
func resolveLoggerSource(spec *Decl, plan *providerPlan) loggerSource {
	prov := plan.loggerProv
	if prov == nil {
		prov = StandardLoggerProvider()
	}
	return providerLoggerSource{prov: prov, spec: spec}
}

// assemble 按契约约定装配 App——段驱动 Provider 装配引擎（原 12 段手工
// 流的整体替换，批 2 W2b；公开入口只剩 Serve/MustServe，见 serve.go）。
//
// spec 为业务能力声明（With*/Providers 写入的 Decl，调用方已 Apply 完毕）；
// cfg 为契约，必须非 nil——Serve 路径由内部契约装载产出（bconf.NewBootstrap +
// 预装载），包内测试经显式注入复刻旧 FromBootstrap(cfg, opts...) 的构造期
// 语义。返回的 App 已完成全部约定装配，直接 Run 即可。
func assemble(spec *Decl, cfg *bootstrapv1.BootstrapConfig) (*App, error) {
	if cfg == nil {
		return nil, errors.New("appkit: assemble: bootstrap config is nil")
	}
	// 段驱动装配计划：路径末者胜 + 分阶段排序（机制见 provider.go）。
	plan, err := newProviderPlan(spec)
	if err != nil {
		return nil, err
	}

	// 域声明解析（装配层评估 §4.3，机制见 domain.go）：装配期校验 fail-fast
	// 先于阶段 A Logger 等一切副作用；Services 并入 gRPC 注册回调（先于下方
	// 能力声明检查——域提供的注册回调与 WithGRPC 声明同权）。
	if err := validateDomains(spec, cfg.GetServer().GetGrpc() != nil); err != nil {
		return nil, err
	}
	composeDomainServices(spec)

	httpCfg := cfg.GetServer().GetHttp()
	grpcCfg := cfg.GetServer().GetGrpc()
	if spec.HTTPHandler != nil && httpCfg == nil {
		return nil, errors.New("appkit: WithHTTP declared but bootstrap.server.http is nil")
	}
	if spec.GatewayRegister != nil && httpCfg == nil {
		return nil, errors.New("appkit: WithGatewayRegister declared but bootstrap.server.http is nil")
	}
	if spec.GRPCRegister != nil && grpcCfg == nil {
		return nil, errors.New("appkit: WithGRPC declared but bootstrap.server.grpc is nil")
	}
	if spec.Health != nil && spec.ProbeLivePath == spec.ProbeReadyPath {
		// 探针要挂在同一个 mux 上，同路径注册会 panic——启动期 fail-fast 暴露。
		return nil, fmt.Errorf("appkit: WithHealth probe paths must differ, got %q twice", spec.ProbeLivePath)
	}
	if spec.GatewayRegister != nil {
		// 网关面由 server.http.driver 选择：非 grpc-gateway 的显式值 = 转码能力
		// 无消费面；与 WithHTTP 同时声明且留空 = 两个能力静默竞争同一端口。
		// 都属启动期错误，fail-fast 暴露（显式 > 隐式）。
		switch d := httpCfg.GetDriver(); {
		case d != "" && d != baldbootstrap.DriverGrpcGateway:
			return nil, fmt.Errorf("appkit: WithGatewayRegister declared but server.http.driver = %q, want %q or empty", d, baldbootstrap.DriverGrpcGateway)
		case spec.HTTPHandler != nil && d == "":
			return nil, fmt.Errorf("appkit: WithHTTP and WithGatewayRegister are both declared; set server.http.driver = %q to serve the transcoding face", baldbootstrap.DriverGrpcGateway)
		}
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

	// 构造期阶段：config 段 Provider 产出配置层（声明序=层优先级，首元素
	// 最高）。scope.App 为 nil——App 尚未构造。失败回滚阶段 A。
	var layers []baldconfig.Layer
	var layersCleanup func()
	if len(plan.construct) > 0 {
		built, _, err := runProviderLoop(context.Background(), plan.construct,
			BuildScope{Config: cfg, Decl: spec}, false, nil)
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
				return nil, fmt.Errorf("appkit: config provider %v returned %T, want []config.Layer", r.prov.Segments(), r.product)
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

	// canonical 停机槽位：各段 Provider 的 cleanup 在 Build 成功后填充，
	// Effect（构造期按 cleanupOrder 预登记）逆序回放时兑现——Effect 注册序
	// 与旧 12 段流逐条一致。
	slots := newCleanupSlots(plan)
	// a 先声明再进闭包：BeforeStart 在 Run 期才执行，届时 a 已赋值。
	var a *App

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
			// 阶段 B Provider 循环：canonical 序（registry→tracer→metrics→
			// database→cache→storage→audit→ai→workflow→broker，见 stageBRank）。
			// 显式 Registrar 实例接管 registry 段时跳过纯 registry Provider
			// （程序化接管 = 刻意覆盖，批 1 语义）。
			bps := plan.stageB
			if spec.Registrar != nil {
				bps = dropSegmentProviders(bps, "registry")
			}
			_, _, err := runProviderLoop(ctx, bps, BuildScope{Config: cfg, Decl: spec, App: a}, true,
				func(r buildResult) error { return storeProduct(a, r, slots) })
			if err != nil {
				return err
			}
			// L3 段覆盖检查（批 2 起报错级，见 segment_coverage.go）：装配链
			// 末尾、契约已装载定型——枚举实际出现的顶层段，减去本次装配真
			// 实被消费的段与白名单，剩余即启动期 error（script 段零消费者的
			// 静默失效由此拦截）。
			return checkSegmentCoverage(cfg, spec, plan)
		}),
		OnConfigChange(func(m map[string]any) {
			hotReload(cfg, spec, logSrc, &curCleanup, m)
		}),
	}
	// 框架停机 Effect：canonical 槽位按 cleanupOrder 依次登记（audit 槽位居
	// 首 → 逆序回放最后执行，后端 cleanup 的 stream 尾批审计事件在指标/span
	// flush 之后收集——回放序：broker → … → database → registrar-client →
	// bootstrap-logger → metrics-server → tracer-shutdown → audit-restore，
	// 与旧 12 段流一致；B8 起无「恢复装配前全局」步骤——全局审计器已删除）。
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
	// 表外（第三方）段 Provider 的停机 Effect：登记在 config-layers 之后、
	// 业务 Effect 之前——逆序回放时业务效果先执行，第三方资源次之，框架
	// 底座（上方 canonical 槽位）最后收。
	kitOpts = append(kitOpts, slots.foreignEffects()...)

	// 业务透传：钩子/协调器/能力声明/订阅/效果——追加在框架装配之后
	//（beforeStart 在内部装载链后执行，可读契约终值；Effect 逆序回放先于
	// 框架 Effect——业务资源先收）。声明经 mech.* 等导出构造面写入 Decl，
	// 引擎经未导出 *Opt 写入面透传（B5 后导出构造面归 mech）。
	for _, fn := range spec.BeforeStart {
		kitOpts = append(kitOpts, BeforeStart(fn))
	}
	// 域装配钩子（装配层评估 §4.3，机制见 domain.go）：注册序在业务钩子之后
	//（域可消费业务钩子建立的运行期资源）、迟后阶段之前（域的路由/服务注册
	// 先于 server 构建生效）——「资源面已就绪」与「注册先于 server 构建」两
	// 约束夹出的窗口。多域次序 = 声明序；域 Wire/Routes 失败 fail-fast。
	if len(spec.Domains) > 0 {
		kitOpts = append(kitOpts, BeforeStart(func(ctx context.Context) error {
			return wireDomains(ctx, a, spec)
		}))
	}
	// 迟后阶段（server 段 Provider）：**追加在业务 beforeStart 之后**——注册序
	// 即执行序，故本钩子在业务钩子后执行，此时业务已建成运行期资源（DB/
	// 仓储/biz/handler），register 回调（gRPC service 注册）与 HTTP handler
	// 消费它们即拿到真实依赖。构造失败的错误经 Run 的 beforeStart 失败路径
	// 回滚（Effect 逆序回放配置层与阶段 A Logger），与旧实现等价。
	if len(plan.late) > 0 {
		kitOpts = append(kitOpts, BeforeStart(func(ctx context.Context) error {
			_, _, err := runProviderLoop(ctx, plan.late, BuildScope{Config: cfg, Decl: spec, App: a}, true,
				func(r buildResult) error { return storeServerProduct(a, r) })
			if err != nil {
				return err
			}
			return nil
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
	return a, nil
}

// dropSegmentProviders 过滤掉「全部声明路径都落在 top 段」的 Provider
// （显式实例接管语义：Registrar 非 nil 时跳过纯 registry Provider）。
func dropSegmentProviders(ps []Provider, top string) []Provider {
	out := ps[:0:0]
	for _, p := range ps {
		all := true
		for _, s := range p.Segments() {
			if segmentTop(s) != top {
				all = false
				break
			}
		}
		if !all {
			out = append(out, p)
		}
	}
	return out
}

// foreignSlotKey 是表外段 Provider 停机槽位的键（首声明路径；同路径多
// Provider 并存时聚合逆序——后建者先收）。
func foreignSlotKey(p Provider) string { return p.Segments()[0] }

// cleanupSlots 是停机清理槽位集：canonical 段按 cleanupOrder 预登记（名字与
// 旧 12 段流逐字一致），表外段按首声明路径预登记；Build 成功后填充，停机
// 逆序回放兑现。
type cleanupSlots struct {
	cleanups map[string]*Cleanup // canonical 段名 → 槽位
	foreign  map[string]*Cleanup // 表外段首路径 → 槽位
}

func newCleanupSlots(plan *providerPlan) *cleanupSlots {
	s := &cleanupSlots{cleanups: map[string]*Cleanup{}, foreign: map[string]*Cleanup{}}
	for _, seg := range cleanupOrder {
		c := Cleanup(nil)
		s.cleanups[seg] = &c
	}
	for _, p := range plan.stageB {
		if _, ok := effectNameBySegment[segmentTop(p.Segments()[0])]; ok {
			continue // canonical 段走上方槽位
		}
		k := foreignSlotKey(p)
		if _, dup := s.foreign[k]; dup {
			continue
		}
		c := Cleanup(nil)
		s.foreign[k] = &c
	}
	return s
}

// fill 填充段槽位（同段多 Provider 并存时聚合逆序：后建者先收）。
func (s *cleanupSlots) fill(seg string, c Cleanup) {
	slot := s.cleanups[seg]
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

// fillForeign 填充表外段槽位（键 = Provider 首声明路径）。
func (s *cleanupSlots) fillForeign(p Provider, c Cleanup) {
	slot := s.foreign[foreignSlotKey(p)]
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

// effects 生成 canonical 段停机 Effect（顺序 = cleanupOrder，即注册序；
// audit 槽位居首 → 逆序回放最后执行）。logger 段非 Provider 驱动，特供
// bootstrap-logger Effect：恢复装配前全局 Logger 并兑现最新后端 cleanup
// （热更重建原子换槽的钩子在此收口）。
func (s *cleanupSlots) effects(oldLogger log.Logger, curCleanup *atomic.Pointer[func()]) []Option {
	var opts []Option
	for _, seg := range cleanupOrder {
		seg := seg
		switch seg {
		case "logger":
			opts = append(opts, effectOpt(effectNameBySegment[seg], func(context.Context) error {
				log.SetLogger(oldLogger)
				if c := curCleanup.Load(); c != nil {
					(*c)()
				}
				return nil
			}))
		default:
			opts = append(opts, effectOpt(effectNameBySegment[seg], func(ctx context.Context) error {
				if c := *s.cleanups[seg]; c != nil {
					return c(ctx)
				}
				return nil
			}))
		}
	}
	return opts
}

// foreignEffects 生成表外段停机 Effect（声明序注册；名字携带段路径便于定位）。
func (s *cleanupSlots) foreignEffects() []Option {
	var opts []Option
	for k, slot := range s.foreign {
		k, slot := k, slot
		opts = append(opts, effectOpt("appkit:provider."+k, func(ctx context.Context) error {
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
	top := segmentTop(r.prov.Segments()[0])
	switch top {
	case "registry":
		reg, ok := r.product.(registry.Registrar)
		if !ok {
			return fmt.Errorf("appkit: registry provider %v returned %T, want registry.Registrar", r.prov.Segments(), r.product)
		}
		a.registrar = reg
		a.setResources(top, reg)
		slots.fill(top, r.cleanup)
	case "database", "cache", "storage", "ai", "workflow", "broker":
		m, err := productStringMap(r)
		if err != nil {
			return err
		}
		a.setResources(top, m)
		slots.fill(top, r.cleanup)
	case "audit":
		aud, ok := r.product.(audit.Auditor)
		if !ok {
			return fmt.Errorf("appkit: audit provider %v returned %T, want audit.Auditor", r.prov.Segments(), r.product)
		}
		// B8：全局 SetAuditor 已删除——契约产物作 appkit 自身审计事件的兜底
		// sink（显式 Auditor Option 优先，不覆盖），并照常落资源面供业务
		// 显式注入 bundle/中间件。
		if a.auditor == nil {
			a.auditor = aud
		}
		a.setResources(top, aud)
		slots.fill(top, r.cleanup)
	case "tracer":
		// 产物不落资源面（tracer shutdown 由 Provider 自持，cleanup 槽位承担
		// 停机回收）。
		slots.fill(top, r.cleanup)
	case "metrics":
		// 产物为可关闭的暴露端（B7：MetricsProvider 返回 MetricsExposure 而非
		// 裸 Handler，监听面归 metricshttp 能力包）。停机序「先停暴露端再
		// flush」由引擎在此合成进 cleanup 槽位——Provider 只需返回 (端点, flush)。
		var exp MetricsExposure
		if r.product != nil {
			e, ok := r.product.(MetricsExposure)
			if !ok {
				return fmt.Errorf("appkit: metrics provider %v returned %T, want MetricsExposure (nil = no exposure)", r.prov.Segments(), r.product)
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
		slots.fill(top, slot)
	default:
		// 表外段：cleanup 照常入账，产物按统一约定落资源面（B4 收口）：
		// map 产物按 "<顶层段>.<子段>" 展开、非 map 产物落 "<顶层段>"。
		a.setResources(top, r.product)
		slots.fillForeign(r.prov, r.cleanup)
	}
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
		return nil, fmt.Errorf("appkit: provider %v returned %T, want map[string]any", r.prov.Segments(), r.product)
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
		return fmt.Errorf("appkit: server provider %v returned %T, want []transport.Server", r.prov.Segments(), r.product)
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
	return rebuildLogger(cfg, logSrc, curCleanup)
}

// hotReload 热更新：副本试装载 + 校验，通过后整契约原子落盘并重建 Logger。
// 任一步失败都降级记日志、保留旧契约，不阻断热更新流程（与审计旁路同哲学）。
// Unmarshal 只做结构转换、不校验取值，坏值必须由 Validate 拦在落盘之前。
func hotReload(cfg *bootstrapv1.BootstrapConfig, spec *Decl, logSrc loggerSource, curCleanup *atomic.Pointer[func()], m map[string]any) {
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

	if err := rebuildLogger(cfg, logSrc, curCleanup); err != nil {
		lg.Error(context.Background(), msg, "err", err)
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
