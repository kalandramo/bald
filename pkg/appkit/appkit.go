// Package appkit 是 bald 框架的组合层（App 层）。
//
// 设计融合三方之长：
//   - onexstack/pkg/app 的 Options + 配置理念（启动期由调用方注入 --config/配置装载器）；
//   - Kratos 的 transport.Server 契约与 registry.Registrar 接口（可插拔复用）；
//   - go-lulu 的自研 App 层精髓：自管 errgroup、优雅停机防坑（Stop 传入未取消 ctx）、
//     崩溃级联停止、Run 防重入、可观察通道、Endpoint 动态端口注册。
//
// 用法：
//
//	app := appkit.New(
//	    appkit.Name("bald-demo"),
//	    appkit.StopTimeout(15*time.Second),
//	    appkit.Registrar(registrar),
//	    appkit.Servers(grpcSrv, httpSrv),
//	)
//	if err := app.Run(context.Background()); err != nil { log.Fatal(err) }
package appkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	kratosRegistry "github.com/go-kratos/kratos/v3/registry"
	bconf "github.com/kalandramo/bald/bconf"
	"github.com/kalandramo/bald/bootstrap/config"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/registry"
	"github.com/kalandramo/bald/transport"
)

// ErrAlreadyRunning 在重复调用 Run 时返回。
var ErrAlreadyRunning = errors.New("appkit: Run already in progress")

// AppKit 自研应用编排层。
type AppKit struct {
	id      string
	name    string
	version string

	// 各生命周期阶段独立超时。
	// stopTimeout 用于服务器 Stop 阶段；before/afterStop 钩子各有独立超时，
	// 避免单个钩子阻塞拖垮整机停机。
	stopTimeout       time.Duration
	beforeStopTimeout time.Duration
	afterStopTimeout  time.Duration

	registrar registry.Registrar // 注册中心
	servers   []transport.Server // 服务协议

	// 钩子（可选）。
	beforeStart []func(context.Context) error
	afterStart  []func(context.Context) error
	beforeStop  []func(context.Context) error
	afterStop   []func(context.Context) error

	// T1 效应账本：全局注册的逆操作登记处，停机/失败回滚时逆序回放（见 effect.go）。
	// TODO 和钩子区别，能否合并？
	effects       []effectEntry
	effectsMu     sync.Mutex
	effectTimeout time.Duration

	// S1 能力声明：Provides/Requires 装配声明，Run 启动早期 Resolve 校验（见 capability.go）。
	// TODO 和服务协议区别，能否合并？
	provides []string
	requires []requirement

	// R1 key 级配置订阅：细粒度变更分发（见 keywatch.go）。
	// TODO 合并到配置管理？
	keyWatchers []keyWatcher
	keyWatchMu  sync.Mutex

	// R1-2 期望态协调器：配置声明期望态，框架 diff 实际态后收敛（见 reconcile.go）。
	// TODO 使用场景列举
	reconcilers []reconciler
	reconItems  map[string][]reconItem // reconciler 名 → 其管理的组件（实际态）
	reconMu     sync.Mutex

	// C1 进程内组件：统一生命周期的基础设施（见 component.go）。
	// TODO 和钩子的区别，使用场景列举
	components       []Component
	componentTimeout time.Duration
	started          []Component // 已成功 Start 的组件（Dispose 幂等跟踪）
	startedMu        sync.Mutex
	disposed         bool // 组件系统已销毁（A1：置位后拒绝运行期挂载，见 mount.go）

	// D4 审计后端：运行期重组/协调审计事件（mount/reconcile）的显式注入点。
	// nil 时回退 audit.GetAuditor() 全局（其默认即 nop）——消除「bundle 注入 a、
	// appkit 走全局」的审计 sink 分裂。
	auditor audit.Auditor

	// 配置对象
	cfg appConfig

	// 可观察性。
	// TODO 使用场景列举
	running atomic.Bool
	done    chan struct{}
	runErr  atomic.Value // error
}

// appConfig 收敛 AppKit 的配置装配输入与加载结果。
// 装配输入：cfgFile / remote / layers（契约源层） / env / watchFile / onChange / bindings；加载结果：store。
type appConfig struct {
	cfgFile   string
	remote    config.RemoteSource
	layers    []config.Layer
	env       string
	watchFile bool
	onChange  func(map[string]any)
	bindings  []flagBinding
	store     *config.Store
}

// closeStore 释放配置仓库的监听资源（fsnotify watcher）；未加载时 no-op。
func (c *appConfig) closeStore() {
	if c.store != nil {
		_ = c.store.Close()
	}
}

// flagBinding 记录一个待绑定进配置装载 FlagSet 的配置对象及其配置键前缀。
// TODO 属于配置管理？
type flagBinding struct {
	prefix string
	opt    any
}

// PlainBinder 支持"无前缀"注册 flag 的配置对象，键前缀由实现体内置。
// 例如 log.Options 固定注册 --log.*。
// TODO 属于配置管理？
type PlainBinder interface {
	AddFlags(fs *pflag.FlagSet)
}

// Bind 把业务配置对象的 flag 注册进 AppKit 的 FlagSet，从而接入配置装载的
// flag 层，使「业务 flag > env > 本地文件 > 远程」这条优先级链完整生效。
//
// 背景：仅调用 AddFlags(pflag.CommandLine, ...) 只把 flag 注册到全局 flagset，
// config.Load 拿不到它们，flag 层实际只有 --config 生效
// （见 docs/devel/zh-CN/配置中心设计.md 第 9.1 节）。Bind 即为此缺口的修复。
//
// 两种 opt：
//  1. proto.Message（全栈 proto 驱动推荐）：Bind("http", &bootstrap.Http)。
//     bconf.BindFlags 会遍历 proto 字段描述符生成带前缀的 flag（--http.addr 等），
//     与配置键路径（http.addr）、环境变量（NAME_HTTP_ADDR）、配置文件一致。
//  2. PlainBinder：opt 自行注册无前缀 flag（键前缀由实现体内置，如 log.Options
//     的 --log.*），此时 prefix 必须传空字符串。
//
// prefix 是配置键前缀（不带末尾点，如 "http"），最终 flag 名为 --http.addr，
// 配置键为 http.addr，环境变量为 NAME_HTTP_ADDR，配置文件为：
//
//	http:
//	  addr: ":8080"
//
// 四者键路径一致，这是 flag 能压过文件与远程的前提。
//
// 用法：
//
//	appkit.Bind("http", &bootstrap.Http),
//	appkit.Bind("grpc", &bootstrap.Grpc),
//	appkit.Bind("", logOpts), // 内置 --log.* 前缀
//
// 注意：用了 Bind 之后不要再自行 AddFlags 到 pflag.CommandLine，否则同一配置
// 有两处注册源（虽然值一致，但语义重复）。
// TODO 和配置管理区别，能否合并？
func Bind(prefix string, opt any) Option {
	return func(a *AppKit) {
		a.cfg.bindings = append(a.cfg.bindings, flagBinding{prefix: prefix, opt: opt})
	}
}

// bindFlags 把一个 flagBinding 注册进给定 FlagSet。
// TODO 和配置管理区别，能否合并？
func bindFlags(fs *pflag.FlagSet, b flagBinding) error {
	switch opt := b.opt.(type) {
	case proto.Message:
		bconf.BindFlags(fs, opt, b.prefix)
	case PlainBinder:
		if b.prefix != "" {
			return fmt.Errorf("appkit: Bind(%q): opt registers its own flag prefix, prefix must be empty", b.prefix)
		}
		opt.AddFlags(fs)
	case nil:
		return fmt.Errorf("appkit: Bind(%q): opt is nil", b.prefix)
	default:
		return fmt.Errorf("appkit: Bind(%q): opt %T is neither a proto.Message (BindFlags) nor a PlainBinder (AddFlags)", b.prefix, b.opt)
	}
	return nil
}

// Option 配置 AppKit。
type Option func(*AppKit)

func ID(id string) Option                   { return func(a *AppKit) { a.id = id } }
func Name(name string) Option               { return func(a *AppKit) { a.name = name } }
func Version(v string) Option               { return func(a *AppKit) { a.version = v } }
func Registrar(r registry.Registrar) Option { return func(a *AppKit) { a.registrar = r } }

// KratosRegistrar 桥接 go-kratos 的 registry.Registrar（etcd/consul 等后端）。
// TODO 合并到注册中心？
func KratosRegistrar(r kratosRegistry.Registrar) Option {
	return func(a *AppKit) { a.registrar = registry.FromKratos(r) }
}

// ConfigFile 指定 --config 配置文件路径（onexstack 风格）。
func ConfigFile(f string) Option { return func(a *AppKit) { a.cfg.cfgFile = f } }

// RemoteConfig 接入远程配置中心（etcd/consul/nacos/apollo/firestore 等）。
// 传入实现 config.RemoteSource 的后端，推荐用 config.FromKratosSource 桥接
// kratos contrib 的 config.Source，例如：
//
//	src := config.FromKratosSource(etcdconfig.New(client, etcdconfig.WithPath("/config/demo/prod.yaml")))
//	appkit.New(..., appkit.RemoteConfig(src))
//
// 语义：远程桥内部被适配为最低优先级的基准层；契约源层推荐用 ConfigLayers
// （由 bootstrap Registry 装配，Registry 注册序即优先级）。
func RemoteConfig(src config.RemoteSource) Option {
	return func(a *AppKit) { a.cfg.remote = src }
}

// ConfigLayers 接入命名配置源层（契约 Config 段 → bconfig 源，bootstrap
// Registry.Build 产出）。列表首元素优先级最高（对齐 Registry 注册序）；
// 整体位于本地文件/env/flag 之下、RemoteConfig 远程桥之上：
//
//	layers, cleanup, err := registry.Build(ctx, bootCfg)
//	defer cleanup()
//	appkit.New(name, appkit.ConfigLayers(layers...))
//
// 任一层 reader 实现 bconfig.ValueWatcher 即参与热更新（变更 → 全量重合并）。
func ConfigLayers(ls ...config.Layer) Option {
	return func(a *AppKit) { a.cfg.layers = append(a.cfg.layers, ls...) }
}

// Env 设置运行环境（dev/test/prod...）。非空时本地按 Name-Env.yaml 选择默认文件，
// 远程 path 由后端构造时拼接（多环境路线 1，详见 docs/config-center-design.md）。
func Env(env string) Option { return func(a *AppKit) { a.cfg.env = env } }

// WatchConfigFile 启用本地配置文件热更新（fsnotify）。
func WatchConfigFile(watch bool) Option { return func(a *AppKit) { a.cfg.watchFile = watch } }

// OnConfigChange 注册配置热更新回调（本地文件或远程变更均触发）。
// 形参为变更后重新合并的配置快照（map），业务在其中重新 Unmarshal 完成热重载。
// 回调应轻量（改状态、切引用、记日志），不要阻塞——它串行阻塞后续变更分发。
func OnConfigChange(fn func(map[string]any)) Option {
	return func(a *AppKit) { a.cfg.onChange = fn }
}

func Servers(servers ...transport.Server) Option {
	return func(a *AppKit) { a.servers = append(a.servers, servers...) }
}

// Auditor 注入运行期审计后端：appkit 自身发出的审计事件（A1 组件挂载/卸载、
// R1-2 协调收敛/错误）写入该实例，与业务经 bundle.Audit 注入的请求级审计
// 同源，消除 split-brain sink（D4）。未注入时回退全局 audit.GetAuditor()。
func Auditor(a audit.Auditor) Option { return func(a2 *AppKit) { a2.auditor = a } }

func StopTimeout(d time.Duration) Option { return func(a *AppKit) { a.stopTimeout = d } }

// BeforeStopTimeout / AfterStopTimeout 设置对应钩子阶段的独立超时（默认 defaultHookTimeout）。
// 与 StopTimeout（服务器 Stop 阶段）分离，避免某个钩子阻塞拖垮整机停机。
func BeforeStopTimeout(d time.Duration) Option {
	return func(a *AppKit) { a.beforeStopTimeout = d }
}

func AfterStopTimeout(d time.Duration) Option {
	return func(a *AppKit) { a.afterStopTimeout = d }
}

// BeforeStart / AfterStart / BeforeStop / AfterStop 注册生命周期钩子。
func BeforeStart(fn func(context.Context) error) Option {
	return func(a *AppKit) { a.beforeStart = append(a.beforeStart, fn) }
}
func AfterStart(fn func(context.Context) error) Option {
	return func(a *AppKit) { a.afterStart = append(a.afterStart, fn) }
}
func BeforeStop(fn func(context.Context) error) Option {
	return func(a *AppKit) { a.beforeStop = append(a.beforeStop, fn) }
}
func AfterStop(fn func(context.Context) error) Option {
	return func(a *AppKit) { a.afterStop = append(a.afterStop, fn) }
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// defaultInstanceID 返回默认实例 ID：hostname + 短随机后缀。
//
// 纯 hostname 会让同机多实例（本地并发多个服务）或同进程多 AppKit 的注册 ID 相同，
// 后注册实例会覆盖前者——服务发现拿到错误端点。随机后缀保证每次启动都是唯一新实例。
// 需要稳定标识（如 K8s 固定 pod 名、跨重启一致）时用 ID() Option 显式注入。
func defaultInstanceID() string {
	return hostname() + "-" + uuid.NewString()[:8]
}

// New 构造 AppKit。
func New(opts ...Option) *AppKit {
	a := &AppKit{
		id:                defaultInstanceID(),
		name:              "bald-app",
		version:           "v0.0.0",
		stopTimeout:       defaultStopTimeout,
		beforeStopTimeout: defaultHookTimeout,
		afterStopTimeout:  defaultHookTimeout,
		effectTimeout:     defaultHookTimeout,
		componentTimeout:  defaultHookTimeout,
		reconItems:        make(map[string][]reconItem),
		done:              make(chan struct{}),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// loadConfig 在启动期加载配置：本地 --config 文件 + 环境变量 + 业务 flag + 可选远程配置中心。
// 结果是 a.cfg.store（*config.Store），调用方通过 Settings()/Config() 读取并
// Unmarshal 到业务 options。
func (a *AppKit) loadConfig() error {
	fs := pflag.NewFlagSet("bald", pflag.ContinueOnError)
	fs.StringVar(&a.cfg.cfgFile, "config", a.cfg.cfgFile,
		"Read configuration from specified FILE (JSON/YAML); "+
			"also supports remote provider if RemoteConfig is set.")
	// 白名单忽略未知 flag，避免未注册的 flag 中断解析。
	fs.ParseErrorsWhitelist.UnknownFlags = true

	// 注册业务 flag：必须进入本 FlagSet（而非仅 pflag.CommandLine），
	// 它才会被 config.Options.Flags 传入装载器落到 flag 层（仅 Changed 的 flag
	// 参与合并），从而压过环境变量、本地文件与远程基准。
	for _, b := range a.cfg.bindings {
		if err := bindFlags(fs, b); err != nil {
			return err
		}
	}

	_ = fs.Parse(os.Args[1:])

	s, err := config.Load(config.Options{
		Name:           a.name,
		Env:            a.cfg.env,
		ConfigFile:     a.cfg.cfgFile,
		Flags:          fs,
		Layers:         a.cfg.layers,
		Remote:         a.cfg.remote,
		WatchLocalFile: a.cfg.watchFile,
		OnChange:       a.wrapKeyWatch(a.cfg.onChange), // R1：包装全量回调，追加 key 级分发
	})
	if err != nil {
		return err
	}
	a.cfg.store = s
	a.armKeyWatchers() // R1：以首次加载值为基线
	return nil
}

// Done 返回应用结束信号通道，便于测试与嵌入。
func (a *AppKit) Done() <-chan struct{} { return a.done }

// Settings 返回加载后的配置快照（深拷贝 map），供 bconf.UnmarshalMap /
// baldconfig.Unmarshal 填充契约。仅在 Run 触发配置加载后才有有效内容；
// 未加载时为 nil。热更新不影响已取出的快照（发布语义：快照稳定）。
func (a *AppKit) Settings() map[string]any {
	if a.cfg.store == nil {
		return nil
	}
	return a.cfg.store.Settings()
}

// Config 返回配置仓库（*config.Store），供点路径实时读取（GetString/
// GetStringSlice/Get）与一站式 Unmarshal。仅在 Run 触发配置加载后非 nil。
func (a *AppKit) Config() *config.Store { return a.cfg.store }

// Err 返回 Run 的退出错误（Run 返回后有效）。
func (a *AppKit) Err() error {
	if v, ok := a.runErr.Load().(error); ok {
		return v
	}
	return nil
}

// Run 启动应用：并发启停所有服务器，处理信号，注册/反注册。
// 当传入的 ctx 被取消或收到 SIGINT/SIGTERM 信号时触发优雅停机。
//
// 行为（参照 go-lulu 固化的契约）：
//   - 防重入：重复调用返回 ErrAlreadyRunning；
//   - 任一服务器 Start 失败 -> 其余服务器都被 Stop（崩溃级联）；
//   - Stop 传入的 ctx 是未取消的新 ctx（带 stopTimeout），stopTimeout 才有效；
//   - 信号（SIGINT/SIGTERM）或 ctx 取消触发优雅停机。
func (a *AppKit) Run(ctx context.Context) error {
	// 先占坑（防重入），再加载配置——顺序重要，不可调换。
	//
	// 曾把 loadConfig 放在 CAS 之前，理由是「配置失败不属于运行中状态，不应占用
	// running/done」。但那样做有数据竞争：两个并发 Run 会同时执行 loadConfig，
	// 并发写 a.cfg.cfgFile（pflag 绑定了其地址）与 a.cfg.store（go test -race 可复现）。
	// loadConfig 是 Run 的一部分，理应受防重入保护。
	if !a.running.CompareAndSwap(false, true) {
		return ErrAlreadyRunning
	}

	// 配置加载失败：归还 running 以便重试，但**不** close done。
	// 此时 done 尚未被"认领"（defer close 还没注册），不 close 就不会出现
	// double close，也就守住了原设计想避免的重入窗口。
	if err := a.loadConfig(); err != nil {
		a.running.Store(false)
		a.runErr.Store(err)
		a.UndoEffects(context.Background()) // T1：装配期全局写入须回滚
		return err
	}

	// 以下 defer 必须在 loadConfig 成功之后注册：确保失败路径不 close done。
	defer a.running.Store(false)
	defer close(a.done)
	defer a.cfg.closeStore() // 释放配置监听资源（fsnotify watcher）

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// S1：能力解析 fail-fast（装配一致性检查，紧随配置加载；缺失依赖在启动期
	// 报清晰错误，而非运行时 nil panic）。
	if err := a.Resolve(); err != nil {
		a.runErr.Store(err)
		a.UndoEffects(context.Background())
		return err
	}

	log.GetLogger().Info(ctx, "appkit starting",
		"name", a.name, "version", a.version, "servers", len(a.servers))

	// beforeStart 钩子。
	for _, fn := range a.beforeStart {
		if err := fn(ctx); err != nil {
			a.runErr.Store(err)
			a.UndoEffects(context.Background()) // T1：未到 stopAll 的失败路径也要回滚账本
			return err
		}
	}

	// C1：顺序启动进程内组件（注册序=依赖建立序；servers 之前——组件是
	// 基础设施）。失败经 stopAll 逆序 Dispose 已启动组件。
	if err := a.startComponents(ctx); err != nil {
		cancel()
		a.stopAll(context.Background())
		a.runErr.Store(err)
		log.GetLogger().Error(ctx, "appkit component start failed", "error", err)
		return err
	}

	// 用 errgroup 并发启动所有服务器：任一 Start 返回非 nil 错误会自动取消
	// gctx，从而级联停止其余服务器（BUG-3 行为）。
	g, gctx := errgroup.WithContext(ctx)
	for _, s := range a.servers {
		s := s
		g.Go(func() error {
			return s.Start(gctx)
		})
	}

	// 服务注册前，必须等待所有 server 真正完成监听（Endpoint 解析出真实端口），
	// 否则绑定 ":0" 的 server 会把 "xxx://:0" 注册出去，导致服务发现拿到无效地址。
	if err := a.waitForEndpoints(gctx); err != nil {
		cancel()
		a.stopAll(context.Background())
		a.runErr.Store(err)
		log.GetLogger().Error(ctx, "appkit wait for endpoints failed", "error", err)
		return err
	}

	// 服务注册（使用 Endpoint 解析后的真实地址，支持 :0 动态端口）。
	if err := a.register(gctx); err != nil {
		cancel()
		a.stopAll(context.Background())
		a.runErr.Store(err)
		log.GetLogger().Error(ctx, "appkit register failed", "error", err)
		return err
	}
	if a.registrar != nil {
		log.GetLogger().Info(ctx, "appkit registered",
			"name", a.name, "id", a.id, "endpoints", a.buildInstance().Endpoints)
	}

	// afterStart 钩子。
	for _, fn := range a.afterStart {
		if err := fn(gctx); err != nil {
			cancel()
			_ = a.deregister(context.Background())
			a.stopAll(context.Background())
			a.runErr.Store(err)
			log.GetLogger().Error(ctx, "appkit afterStart hook failed", "error", err)
			return err
		}
	}

	// R1-2：启动后首次协调——让初始配置的期望态立即生效（此后由配置变更触发）。
	// 挂在 afterStart 之后：此时 bridges/组件已就绪，协调可安全调 Start/Dispose。
	a.runReconcilers(gctx)

	log.GetLogger().Info(ctx, "appkit started", "servers", len(a.servers))

	// 主等待：任一服务器崩溃（gctx 取消）、外部 ctx 取消、或系统信号。
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)

	select {
	case <-gctx.Done(): // 服务器崩溃或外部 ctx 取消
	case s := <-sig:
		log.GetLogger().Info(ctx, "appkit received signal, shutting down", "signal", s.String())
		cancel() // 收到信号，主动取消
	}

	// 先反注册（避免流量打到已停服务），再优雅停机。
	if a.registrar != nil {
		log.GetLogger().Info(ctx, "appkit deregistering", "name", a.name, "id", a.id)
	}
	_ = a.deregister(context.Background())

	// 优雅停机：传入未取消的新 ctx（带 stopTimeout），这是 BUG-1 防坑点。
	a.stopAll(context.Background())

	// 收集启动错误：仅非 ctx 取消的错误视为致命，返回给调用方。
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		a.runErr.Store(err)
		log.GetLogger().Error(ctx, "appkit exited with error", "error", err)
		return err
	}
	log.GetLogger().Info(ctx, "appkit stopped")
	return nil
}

// stopAll 分五阶段优雅停机（对照 go-lulu 分阶段停机骨架）：
//  0. 效应账本逆序回放（T1：撤销装配期全局写入，先于一切停机钩子）
//  1. BeforeStop 钩子（独立超时 beforeStopTimeout，panic 安全不拖垮整机）
//  2. 各 Server.Stop 并发（独立超时 stopTimeout）
//  3. AfterStop 钩子（独立超时 afterStopTimeout，panic 安全）
//  4. 组件 Dispose 逆序（C1：AfterStop 之后——钩子期间组件仍可用，最后收尾）
//
// 每个阶段使用各自独立的 WithTimeout ctx，互不影响；parent 取消会级联缩短各阶段。
// waitForEndpoints / register / afterStart / 组件启动失败路径也经本函数回滚
// （含效应回放与组件 Dispose）。
func (a *AppKit) stopAll(parent context.Context) {
	// 阶段 0：效应账本逆序回放（幂等，已回放过则无操作）。
	a.UndoEffects(parent)

	// 阶段 1：BeforeStop 钩子。
	for _, fn := range a.beforeStop {
		if err := a.runHook(parent, a.beforeStopTimeout, "beforeStop", fn); err != nil {
			log.GetLogger().Error(parent, "appkit beforeStop hook failed", "error", err)
		}
	}

	// 阶段 2：并发停止所有服务器（各自共享 stopTimeout ctx）。
	stopCtx, cancel := context.WithTimeout(parent, a.stopTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, s := range a.servers {
		wg.Add(1)
		go func(s transport.Server) {
			defer wg.Done()
			if err := s.Stop(stopCtx); err != nil {
				log.GetLogger().Error(stopCtx, "appkit server stop failed", "error", err)
			}
		}(s)
	}
	wg.Wait()

	// 阶段 3：AfterStop 钩子。
	for _, fn := range a.afterStop {
		if err := a.runHook(parent, a.afterStopTimeout, "afterStop", fn); err != nil {
			log.GetLogger().Error(parent, "appkit afterStop hook failed", "error", err)
		}
	}

	// 阶段 4：组件 Dispose（C1，逆序、幂等；AfterStop 之后——钩子期间组件仍可用）。
	a.disposeComponents(parent)
}

// runHook 在独立超时 ctx 中执行生命周期钩子，并 recover 防止单个钩子 panic 拖垮整机。
func (a *AppKit) runHook(parent context.Context, timeout time.Duration, name string, fn func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("appkit %s hook panicked: %v", name, r)
		}
	}()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return fn(ctx)
}

// buildInstance 聚合所有 Server 的 Endpoint 构造 ServiceInstance。
func (a *AppKit) buildInstance() *registry.ServiceInstance {
	var eps []string
	for _, s := range a.servers {
		if ep := s.Endpoint(); ep != "" {
			eps = append(eps, ep)
		}
	}
	kind := "mixed"
	if len(a.servers) == 1 {
		kind = "single"
	}
	return &registry.ServiceInstance{
		ID:        a.id,
		Name:      a.name,
		Version:   a.version,
		Kind:      kind,
		Metadata:  map[string]string{"scheme": kind},
		Endpoints: eps,
	}
}

// waitForEndpoints 轮询直到所有 server 的 Endpoint 解析出真实端口（非 ":0"），
// 或 ctx 取消/超时。这避免了把 ":0" 这样的未绑定地址注册到服务发现。
// 对于显式绑定固定端口的 server，Endpoint 一开始就非 ":0"，会立即通过。
func (a *AppKit) waitForEndpoints(ctx context.Context) error {
	const (
		pollInterval = 10 * time.Millisecond
		timeout      = 5 * time.Second // 单个 server 监听准备上限
	)
	deadline := time.Now().Add(timeout)
	for {
		ready := true
		for _, s := range a.servers {
			ep := s.Endpoint()
			// 未启动的 server 返回 "scheme://:0" 或空，视为未就绪。
			if ep == "" || strings.HasSuffix(ep, ":0") {
				ready = false
				break
			}
		}
		if ready {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("appkit: servers not ready within %s (dynamic port not bound)", timeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("appkit: wait for endpoints canceled: %w", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// register 将实例注册到服务发现（若有 Registrar）。
func (a *AppKit) register(ctx context.Context) error {
	if a.registrar == nil {
		return nil
	}
	return a.registrar.Register(ctx, a.buildInstance())
}

// deregister 从服务发现注销实例（优先调用，避免流量打到已停服务）。
func (a *AppKit) deregister(ctx context.Context) error {
	if a.registrar == nil {
		return nil
	}
	return a.registrar.Deregister(ctx, a.buildInstance())
}
