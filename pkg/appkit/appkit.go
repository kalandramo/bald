// Package appkit 是 bald 框架的组合层（App 层）。
//
// 设计融合三方之长：
//   - onexstack/pkg/app 的 Options + 配置理念（启动期由调用方注入 --config/viper）；
//   - Kratos 的 transport.Server 契约与 registry.Registrar 接口（可插拔复用）；
//   - go-lulu 的自研 App 层精髓：自管 errgroup、优雅停机防坑（Stop 传入未取消 ctx）、
//     崩溃级联停止、Run 防重入、可观察通道、Endpoint 动态端口注册。
//
// 与初版不同，本实现不再"薄包装 kratos.App"，而是自研编排逻辑，
// 因此保留全部精细控制，且仅可选依赖 kratos 的 registry 接口。
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

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"

	kratosRegistry "github.com/go-kratos/kratos/v3/registry"
	"github.com/kalandramo/bald/pkg/config"
	"github.com/kalandramo/bald/pkg/log"
	"github.com/kalandramo/bald/pkg/registry"
	"github.com/kalandramo/bald/pkg/server"
)

// 默认优雅停机超时。
const defaultStopTimeout = 10 * time.Second

// ErrAlreadyRunning 在重复调用 Run 时返回。
var ErrAlreadyRunning = errors.New("appkit: Run already in progress")

// AppKit 自研应用编排层。
type AppKit struct {
	id          string
	name        string
	version     string
	registrar   registry.Registrar
	servers     []server.Server
	stopTimeout time.Duration

	// 钩子（可选）。
	beforeStart []func(context.Context) error
	afterStart  []func(context.Context) error
	beforeStop  []func(context.Context) error
	afterStop   []func(context.Context) error

	// 配置（onexstack 风格 --config + 可选远程配置中心），收敛为一个配置对象。
	cfg appConfig

	// 可观察性。
	running atomic.Bool
	done    chan struct{}
	runErr  atomic.Value // error
}

// appConfig 收敛 AppKit 的配置装配输入与加载结果。
// 装配期输入：cfgFile / remote / env / watchFile / onChange；加载结果：v。
type appConfig struct {
	cfgFile   string
	remote    config.RemoteSource
	env       string
	watchFile bool
	onChange  func(*viper.Viper)
	v         *viper.Viper
}

// Option 配置 AppKit。
type Option func(*AppKit)

func ID(id string) Option            { return func(a *AppKit) { a.id = id } }
func Name(name string) Option        { return func(a *AppKit) { a.name = name } }
func Version(v string) Option        { return func(a *AppKit) { a.version = v } }
func Registrar(r registry.Registrar) Option { return func(a *AppKit) { a.registrar = r } }

// KratosRegistrar 桥接 go-kratos 的 registry.Registrar（etcd/consul 等后端）。
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
func RemoteConfig(src config.RemoteSource) Option {
	return func(a *AppKit) { a.cfg.remote = src }
}

// Env 设置运行环境（dev/test/prod...）。非空时本地按 Name-Env.yaml 选择默认文件，
// 远程 path 由后端构造时拼接（多环境路线 1，详见 docs/config-center-design.md）。
func Env(env string) Option { return func(a *AppKit) { a.cfg.env = env } }

// WatchConfigFile 启用本地配置文件热更新（fsnotify）。
func WatchConfigFile(watch bool) Option { return func(a *AppKit) { a.cfg.watchFile = watch } }

// OnConfigChange 注册配置热更新回调（本地文件或远程变更均触发）。
func OnConfigChange(fn func(*viper.Viper)) Option {
	return func(a *AppKit) { a.cfg.onChange = fn }
}

func Servers(servers ...server.Server) Option {
	return func(a *AppKit) { a.servers = append(a.servers, servers...) }
}
func StopTimeout(d time.Duration) Option { return func(a *AppKit) { a.stopTimeout = d } }

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

// New 构造 AppKit。
func New(opts ...Option) *AppKit {
	a := &AppKit{
		id:          hostname(),
		name:        "bald-app",
		version:     "v0.0.0",
		stopTimeout: defaultStopTimeout,
		done:        make(chan struct{}),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// loadConfig 在启动期加载配置：本地 --config 文件 + 环境变量 + 可选远程配置中心。
// 结果是 a.cfg.v（*viper.Viper），调用方通过 Viper() 读取并 Unmarshal 到业务 options。
func (a *AppKit) loadConfig() error {
	fs := pflag.NewFlagSet("config", pflag.ContinueOnError)
	fs.StringVar(&a.cfg.cfgFile, "config", a.cfg.cfgFile,
		"Read configuration from specified FILE (JSON/TOML/YAML/HCL/properties); "+
			"also supports remote provider if RemoteConfig is set.")
	// 解析命令行 --config（其余业务 flag 如 --http.addr 由调用方自行绑定/解析，
	// 这里白名单忽略未知 flag，避免因未注册 flag 中断解析）。
	fs.ParseErrorsWhitelist.UnknownFlags = true
	_ = fs.Parse(os.Args[1:])

	v, err := config.Load(config.Options{
		Name:           a.name,
		Env:            a.cfg.env,
		ConfigFile:     a.cfg.cfgFile,
		Flags:          fs,
		Remote:         a.cfg.remote,
		WatchLocalFile: a.cfg.watchFile,
		OnChange:       a.cfg.onChange,
	})
	if err != nil {
		return err
	}
	a.cfg.v = v
	return nil
}

// Done 返回应用结束信号通道，便于测试与嵌入。
func (a *AppKit) Done() <-chan struct{} { return a.done }

// Viper 返回加载后的配置实例，供调用方 Unmarshal 到业务 options 结构体。
// 仅在 Run 触发配置加载后才有有效内容；未配置加载时为 nil。
func (a *AppKit) Viper() *viper.Viper { return a.cfg.v }

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
	// 启动期：加载配置（onexstack 风格 --config + 可选远程配置中心）。
	// 放在防重入 CAS 之前：配置失败不属于"运行中"状态，不应占用 running/done，
	// 否则失败即重置 running 会打开重入窗口（可能 double close(done)）。
	if err := a.loadConfig(); err != nil {
		a.runErr.Store(err)
		return err
	}

	if !a.running.CompareAndSwap(false, true) {
		return ErrAlreadyRunning
	}
	defer a.running.Store(false)
	defer close(a.done)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	log.GetLogger().Info(ctx, "appkit starting",
		"name", a.name, "version", a.version, "servers", len(a.servers))

	// beforeStart 钩子。
	for _, fn := range a.beforeStart {
		if err := fn(ctx); err != nil {
			a.runErr.Store(err)
			return err
		}
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

// stopAll 优雅停止所有服务器。stopCtx 必须是未取消的 ctx，确保 stopTimeout 生效。
func (a *AppKit) stopAll(parent context.Context) {
	for _, fn := range a.beforeStop {
		_ = fn(parent)
	}

	stopCtx, cancel := context.WithTimeout(parent, a.stopTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, s := range a.servers {
		wg.Add(1)
		go func(s server.Server) {
			defer wg.Done()
			_ = s.Stop(stopCtx)
		}(s)
	}
	wg.Wait()

	for _, fn := range a.afterStop {
		_ = fn(parent)
	}
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
