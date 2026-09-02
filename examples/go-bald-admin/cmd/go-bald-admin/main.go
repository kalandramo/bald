// Command go-bald-admin 是 go-bald-admin 服务入口：
// 用 bald appkit 组合一个 HTTP 服务与一个 gRPC 服务（双协议），
// 作为用 bald 重构 go-wind-admin/backend 的官方范例（验证 P0–P9）。
//
// 运行：
//
//	go run ./examples/go-bald-admin --config=examples/go-bald-admin/configs/go-bald-admin.yaml
//	go run ./examples/go-bald-admin --http.addr=:18080
//	BALD_HTTP_ADDR=:18080 go run ./examples/go-bald-admin
//
//	# 验证 HTTP 路由
//	curl -i http://127.0.0.1:8080/v1/ping
//	curl -i http://127.0.0.1:8080/v1/info
//
// 设计见 examples/go-bald-admin/docs/设计文档.md；分层参考 osbuilder 脚手架模板。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"google.golang.org/grpc"

	adminv1 "github.com/kalandramo/bald/examples/go-bald-admin/gen/secretv1"
	"github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver"
	secretgrpc "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/grpc"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"

	obmetrics "github.com/kalandramo/bald-observability-otlp/metrics"
	obtrace "github.com/kalandramo/bald-observability-otlp/trace"
	securityaudit "github.com/kalandramo/bald/examples/go-bald-admin/internal/security/audit"
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	baldconf "github.com/kalandramo/bald/pkg/conf"
	confv1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/config/v1"
	baldconfig "github.com/kalandramo/bald/pkg/config"
	baldlog "github.com/kalandramo/bald/pkg/log"
	"github.com/kalandramo/bald/pkg/middleware/bundle"
	"github.com/kalandramo/bald/pkg/registry/inmemory"
	"github.com/kalandramo/bald/pkg/server"
)

func serveRunE(_ *cobra.Command, _ []string) error {
	// 0. 框架级配置：proto 是唯一真相源，直接持有 Bootstrap 指针。
	bootstrap := baldconf.NewBootstrap()
	bootstrap.Http.Addr = ":8080"

	// 日志系统接入（两阶段：先默认，配置加载后重建）。
	logOpts := baldlog.NewOptions()
	setLogger(logOpts)

	// M8/M9 可观测性：初始化 MeterProvider 并启动 /metrics 端点（独立端口，供 Prometheus 抓取）。
	// M9 延伸 trace OTLP 直推：先设全局 TracerProvider（核心 span 已埋点但默认 no-op，需本步接线）。
	// 两者均按 BALD_ADMIN_OTLP_ADDR 开关（P11 起实现晋升 contrib bald-observability-otlp，
	// 环境变量读取上移到本入口）；须在拦截器构建前 Setup，使埋点接入 exporter。
	otlpAddr := os.Getenv("BALD_ADMIN_OTLP_ADDR")
	traceShutdown, err := obtrace.Setup(
		obtrace.WithOTLPAddr(otlpAddr),
		obtrace.WithServiceName("go-bald-admin"),
	)
	if err != nil {
		return fmt.Errorf("setup trace: %w", err)
	}
	// C1：trace provider 注册为进程内组件（退出时由 appkit 统一逆序 Dispose flush
	// 尾批 span——此前手工塞 BeforeStop 的 traceShutdownFn 已删除，「忘 flush 丢批」
	// 从文档知识变成结构保证）。
	traceComp := appkit.ComponentFunc("trace.provider", traceShutdown)

	metricsAddr := os.Getenv("BALD_ADMIN_METRICS_ADDR")
	if metricsAddr == "" {
		metricsAddr = ":9090"
	}
	metricsHandler, err := obmetrics.Setup(
		obmetrics.WithOTLPAddr(otlpAddr),
		obmetrics.WithServiceName("go-bald-admin"),
	)
	if err != nil {
		return fmt.Errorf("setup metrics: %w", err)
	}
	go func() {
		mSrv := &http.Server{Addr: metricsAddr, Handler: metricsHandler}
		if serveErr := mSrv.ListenAndServe(); serveErr != nil && serveErr != http.ErrServerClosed {
			baldlog.GetLogger().Error(context.Background(), "metrics server stopped", "error", serveErr.Error())
		}
	}()

	// 1. 业务装配（分层见 internal/apiserver）。M6.4 起由 wire 显式拼装业务对象
	//    （cache / auth biz / secret biz），编译期依赖图校验；框架桥接仍在 appkit.BeforeStart 注入。
	bizSet, err := InitializeBiz()
	if err != nil {
		return fmt.Errorf("initialize biz (wire): %w", err)
	}
	ready := func(ctx context.Context) error { return nil }

	// M10.2 管理面：运行期组件观测与热插拔（工厂目录由业务定义——核心只管挂载原语）。
	// demo.heartbeat 演示带 goroutine 的组件生命周期（Start 起心跳、Dispose 收），与
	// StreamAuditor 同构；admin 角色经 /admin/components 端点挂载/卸载。
	appRef := &appRefT{}
	componentFactories := map[string]apiserver.ComponentFactory{
		"demo.heartbeat": func() appkit.Component { return newHeartbeatComponent(appRef.get) },
	}

	// M10.1（P10 验证）：横切关注点切 bundle 门面。
	//   - gin 全局层：Recovery→RequestID→Logging→Audit（bundle 链序固化）。
	//     Authn/Authz 不进全局 bundle——范例用「路由级分组保护」语义（/v1/login 必须公开），
	//     分组链见 internal/apiserver/handler/gin/auth.go；bundle 的全局链模式与
	//     分组保护模式是两种合法模式，范例各保其一（gRPC 侧为全 bundle 链）。
	//   - 增强点：切 bundle 后 /v1/login 也进入审计（此前散装手挂仅审计受保护路由）——
	//     登录失败同样应留审计痕迹。
	ginBundle := bundle.New(
		bundle.Metrics(obmetrics.Recorder("bald/example")),
		bundle.Normalized(), // P9 归一化：审计 object/action 与 gRPC 同源
	)
	router := gin.New()
	router.Use(ginBundle.Gin()...)
	apiserver.RegisterRoutes(router, bizSet.Auth, bizSet.Secret) // gin handler 路由
	registerAdminRoutes(router, appRef, componentFactories)      // M10.2 管理面（appRef 迟到绑定）
	httpSrv := server.NewHTTPServer(bootstrap.GetHttp(), router, ready)

	grpcSrv := server.NewGRPCServerWithRegister(
		bootstrap.GetGrpc(),
		newGRPCServerOptions(),
		registerGRPCService,
		ready,
	)

	// 2. 组装 AppKit（含可选 grpc-gateway，见 buildServers）。
	app := newApp(bootstrap, logOpts, httpSrv, grpcSrv, ready, traceComp)
	appRef.set(app) // M10.2：管理面 handler 经 appRef 请求期取 AppKit（规避装配时序）

	// 3. 运行。
	if err := app.Run(context.Background()); err != nil {
		baldlog.GetLogger().Error(context.Background(), "go-bald-admin exited", "error", err)
		return err
	}
	return nil
}

func main() {
	root := &cobra.Command{
		Use:   "go-bald-admin",
		Short: "go-bald-admin 服务（bald 重构范例）",
		RunE:  serveRunE,
	}
	// appkit 自行解析 os.Args 中的 --config / --http.addr 等业务 flag（见 appkit.loadConfig），
	// 故 cobra 需放行未知 flag，避免它因不识别 --config 而提前报错退出。
	root.FParseErrWhitelist.UnknownFlags = true

	// kubectl 风格插件发现：首参非已知子命令（且非 flag）时转发到 PATH 中的 go-bald-admin-<name>。
	if len(os.Args) > 1 {
		sub := os.Args[1]
		if !strings.HasPrefix(sub, "-") && !isKnownCommand(root, sub) {
			plugin := "go-bald-admin-" + sub
			path, err := exec.LookPath(plugin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "unknown command %q (and no plugin %q found in PATH)\n", sub, plugin)
				if err := root.Execute(); err != nil {
					osExit(1)
				}
				return
			}
			cmd := exec.Command(path, os.Args[2:]...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				osExit(1)
			}
			return
		}
	}

	if err := root.Execute(); err != nil {
		osExit(1)
	}
}

func isKnownCommand(root *cobra.Command, name string) bool {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return true
		}
	}
	return false
}

func newApp(
	bootstrap *confv1.Bootstrap,
	logOpts *baldlog.Options,
	httpSrv *server.HTTPServer,
	grpcSrv *server.GRPCServer,
	ready server.ReadinessFunc,
	traceComp appkit.Component,
) *appkit.AppKit {
	var app *appkit.AppKit
	app = appkit.New(
		appkit.Name("go-bald-admin"),
		appkit.Version("v0.1.0"),
		appkit.StopTimeout(15*time.Second),

		appkit.ConfigFile("configs/go-bald-admin.yaml"),
		appkit.WatchConfigFile(true),

		// S1 能力声明（启动期 fail-fast）：BeforeStart 的 InitBridges 将建立真实 DB
		// 连接（BALD_ADMIN_DB_DSN，缺省 SQLite 内存），审计落库（StoreAuditor）依赖它。
		// 此前「审计后端须在 InitBridges 之后装配」只活在注释里（M8 曾因顺序反了
		// nil panic）；现在漏掉 Provides("db") 会在 Run 启动期直接报错，而非运行时炸弹。
		appkit.Provides("db"),
		appkit.Requires("audit.store", "db"),

		// C1 进程内组件：trace provider 纳入统一生命周期（停机末段逆序 Dispose）。
		appkit.Components(traceComp),

		// 业务 flag 接入：前缀与配置键一致（--http.addr ⇔ http.addr ⇔ BALD_HTTP_ADDR）。
		appkit.Bind("http", bootstrap.GetHttp()),
		appkit.Bind("grpc", bootstrap.GetGrpc()),
		appkit.Bind("", logOpts),

		appkit.OnConfigChange(func(v *viper.Viper) {
			logger := baldlog.GetLogger()
			logger.Info(context.Background(), "config changed",
				"http.addr", v.GetString("http.addr"), "grpc.addr", v.GetString("grpc.addr"))
			if err := baldconfig.Unmarshal(v, bootstrap); err != nil {
				logger.Error(context.Background(), "reload config failed", "error", err)
			}
		}),

		// R1 增量协调（key 级订阅）：仅当 http.addr 实际变化才触发（同值刷新、
		// 其他 key 变更均不波及），与全量 reload 互补——全量做 Unmarshal 重建，
		// key 级做定点观测/定点重载。
		appkit.OnKeyChange("http.addr", func(old, new string) {
			baldlog.GetLogger().Info(context.Background(), "http.addr changed",
				"old", old, "new", new)
		}),

		appkit.Registrar(inmemory.New()),
		appkit.Servers(buildServers(bootstrap, httpSrv, grpcSrv, ready)...),

		appkit.BeforeStart(func(ctx context.Context) error {
			v := app.Viper()
			if v == nil {
				return nil
			}
			if err := baldconfig.Unmarshal(v, bootstrap); err != nil {
				return fmt.Errorf("unmarshal config: %w", err)
			}
			if err := baldconf.Validate(bootstrap); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}
			// 阶段 B：按最终配置重建 Logger，并装配 bald 桥接（P7/P8/P9 注册点）。
			setLogger(baldconf.LogOptions(bootstrap.GetLogger()))
			// 在 bootstrap 包内装配 bald 桥接（P7/P8/P9 注册点）：M1+ 注入
			// Authenticator / Authorizer / store.RegisterTenant / store.RegisterDataScope。
			if err := bootstrappkg.InitBridges(ctx); err != nil {
				return fmt.Errorf("init bridges: %w", err)
			}
			// 审计后端注入（R1-2 期望态协调）改由 appkit.Reconcile 驱动：声明期望集合
			// （audit.backends）后，框架在启动收敛期与每次配置变更时自动 diff-apply，
			// 此处不再静态装配——单一事实来源收敛到配置键，避免“配置与代码两处声明”。
			return nil
		}),
		// R1-2 期望态协调：以 audit.backends 为期望态，与当前生效后端集合 diff，
		// 自动重建 MultiAuditor 收敛（幂等）。首次收敛在 AfterStart 前（runReconcilers
		// 于基线 loadConfig 后、BeforeStart 链之后触发）；后续 OnConfigChange 携带新
		// viper 再次触发，实现运行期热切换而无需重启。
		appkit.Reconcile("audit.backends", reconcileAudit),
		appkit.AfterStart(func(ctx context.Context) error {
			ctx = baldlog.ContextWithAttrs(ctx,
				slog.String("stage", "started"), slog.String("grpc", grpcSrv.Endpoint()))
			baldlog.GetLogger().Info(ctx, "go-bald-admin started", "http", httpSrv.Endpoint())
			return nil
		}),
		appkit.BeforeStop(func(ctx context.Context) error {
			baldlog.GetLogger().Info(ctx, "go-bald-admin stopping")
			return nil
		}),
	)
	return app
}

// reconAuditors 是 R1-2 协调的「实际态」载体：协调器逐后端 Mount/Unmount，
// 每个后端组件在 Start 时把自己塞入此表、Dispose 时移除，并刷新全局 MultiAuditor。
// 由 ReconcileCtx.Mount/Unmount（底层 A1 组件生命周期 + reconItems 归属表）串行化，
// 自身无需额外加锁——协调器单次执行是串行的，且框架对多协调器按注册序串行。
var reconAuditors = struct {
	mu  sync.Mutex
	set map[string]audit.Auditor
}{set: make(map[string]audit.Auditor)}

// applyAuditors 根据当前后端表重建全局 MultiAuditor（按后端名排序，顺序确定、
// 便于测试与排查；空则退化为 Nop，绝不阻断审计旁路）。
func applyAuditors() {
	reconAuditors.mu.Lock()
	names := make([]string, 0, len(reconAuditors.set))
	for n := range reconAuditors.set {
		names = append(names, n)
	}
	sort.Strings(names)
	list := make([]audit.Auditor, 0, len(names))
	for _, n := range names {
		list = append(list, reconAuditors.set[n])
	}
	reconAuditors.mu.Unlock()
	if len(list) == 0 {
		audit.SetAuditor(audit.NopAuditor())
		return
	}
	audit.SetAuditor(securityaudit.NewMulti(list...))
}

// auditBackendComponent 是一个后端审计器组件：Mount 即把自身审计器注册进全局
// MultiAuditor，Unmount 即注销。其名 == 后端名（log/store/stream），正是协调器
// 用于 diff 的标识，与 ReconcileCtx.Mounted() 一一对应。
type auditBackendComponent struct {
	name string
	build func() audit.Auditor // 延迟构造：依赖 InitBridges 后的 DB/Redis
}

func (c *auditBackendComponent) Name() string { return c.name }

func (c *auditBackendComponent) Start(ctx context.Context) error {
	a := c.build()
	if a == nil {
		return fmt.Errorf("audit backend %q unavailable", c.name)
	}
	reconAuditors.mu.Lock()
	reconAuditors.set[c.name] = a
	reconAuditors.mu.Unlock()
	applyAuditors()
	baldlog.GetLogger().Info(ctx, "audit backend mounted", "backend", c.name)
	return nil
}

func (c *auditBackendComponent) Dispose(ctx context.Context) error {
	reconAuditors.mu.Lock()
	delete(reconAuditors.set, c.name)
	reconAuditors.mu.Unlock()
	applyAuditors()
	baldlog.GetLogger().Info(ctx, "audit backend unmounted", "backend", c.name)
	return nil
}

// buildAuditBackend 构造某后端的审计器（log 始终可用；store/stream 依赖桥接就绪）。
// 桥接未就绪时返回 nil，协调器会跳过该后端（旁路语义，绝不阻断启动）。
func buildAuditBackend(name string) audit.Auditor {
	switch name {
	case "log":
		return securityaudit.New()
	case "store":
		if bootstrappkg.DB != nil {
			return securityaudit.NewStore(bootstrappkg.DB)
		}
	case "stream":
		if bootstrappkg.RedisCache != nil && bootstrappkg.RedisCache.Client() != nil {
			return securityaudit.NewStream(bootstrappkg.RedisCache.Client())
		}
	}
	return nil
}

// reconcileAudit 是 R1-2 期望态协调函数（逐后端粒度，非整体重建）：配置键
// audit.backends 声明「期望的审计后端集合」，框架用 ReconcileCtx 暴露的实际态
// （r.Mounted()，即本协调器已挂载的后端名）做 diff，仅 Mount 新增、Unmount 移除——
// 完整兑现「只更新变动部分」的 K8s controller 语义。部分失败不回滚，下次协调补齐。
//
// 触发时机由框架负责：首次在 loadConfig 基线后、AfterStart 前的启动收敛期；其后
// 每次 OnConfigChange 携带新 viper 再次调用，实现运行期热切换（如 [store]→[store,stream]）。
func reconcileAudit(ctx context.Context, rctx *appkit.ReconcileCtx) error {
	want := parseAuditBackends(rctx.Viper.GetString("audit.backends"))
	have := rctx.Mounted() // 实际态：本协调器名下已挂载的后端（Mount/Unmount 维护）

	// diff-apply：期望==实际时直接返回（幂等，不抖动、不发多余审计）。
	add, remove := appkit.DiffStrings(want, have)
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}

	baldlog.GetLogger().Info(ctx, "reconcile audit.backends",
		"add", strings.Join(add, ","), "remove", strings.Join(remove, ","))

	// 新增：期望有、实际无 → 逐后端 Mount（底层 A1 组件生命周期 + 重组审计）。
	for _, name := range add {
		comp := &auditBackendComponent{name: name, build: func() audit.Auditor { return buildAuditBackend(name) }}
		if err := rctx.Mount(ctx, comp.Name(), comp); err != nil {
			// 失败不回滚，下次协调按实际态（reconItems）重新 diff 补齐。
			baldlog.GetLogger().Error(ctx, "reconcile mount audit backend failed",
				"backend", name, "error", err)
		}
	}
	// 移除：实际有、期望无 → 逐后端 Unmount。
	for _, name := range remove {
		if err := rctx.Unmount(ctx, name); err != nil {
			baldlog.GetLogger().Error(ctx, "reconcile unmount audit backend failed",
				"backend", name, "error", err)
		}
	}
	return nil
}

// parseAuditBackends 解析逗号/空格分隔的后端列表，过滤非法取值，去重保序。
func parseAuditBackends(s string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, raw := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		b := strings.TrimSpace(raw)
		if b == "" || (b != "log" && b != "store" && b != "stream") {
			continue
		}
		if _, ok := seen[b]; ok {
			continue
		}
		seen[b] = struct{}{}
		out = append(out, b)
	}
	return out
}

// contains 判断切片是否含某元素。
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

var osExit = func(code int) { os.Exit(code) }

// appRefT 是 AppKit 的线程安全迟到绑定句柄：管理面路由在 appkit.New 之前注册
// （gin 装配先于 app 构造），handler 闭包捕获 appRef、请求期读取最新 App 实例。
type appRefT struct {
	mu  sync.RWMutex
	app *appkit.AppKit
}

func (r *appRefT) set(a *appkit.AppKit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.app = a
}

func (r *appRefT) get() *appkit.AppKit {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.app
}

// heartbeatComp 是 M10.2 的演示组件：Start 起周期心跳 goroutine，Dispose 停止并
// 等待退出——与 StreamAuditor 同构的「有 goroutine 要收」组件生命周期范本，
// 经管理面挂载/卸载可端到端观察 A1 的 Start/Dispose 与重组审计。
type heartbeatComp struct {
	name  string
	stop  chan struct{}
	done  chan struct{}
	appFn func() *appkit.AppKit
}

func newHeartbeatComponent(appFn func() *appkit.AppKit) appkit.Component {
	return &heartbeatComp{
		name:  "demo.heartbeat",
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
		appFn: appFn,
	}
}

func (h *heartbeatComp) Name() string { return h.name }

func (h *heartbeatComp) Start(_ context.Context) error {
	go func() {
		defer close(h.done)
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-h.stop:
				return
			case <-t.C:
				baldlog.GetLogger().Info(context.Background(), "component heartbeat",
					"component", h.name, "components", len(h.appFn().ListComponents()))
			}
		}
	}()
	return nil
}

func (h *heartbeatComp) Dispose(_ context.Context) error {
	close(h.stop)
	<-h.done // 等 goroutine 退出：Dispose 后组件对进程无残留影响（时间可组合性）
	return nil
}

// registerAdminRoutes 把管理面路由挂到 router（appRef 迟到绑定见 appRefT）。
func registerAdminRoutes(router *gin.Engine, ref *appRefT, factories map[string]apiserver.ComponentFactory) {
	apiserver.RegisterAdmin(router, ref.get, factories)
}

// registerGRPCService 是 gRPC service 注册回调（M5 用 proto 生成的 SecretServiceServer）。
var registerGRPCService = func(s *grpc.Server) {
	adminv1.RegisterSecretServiceServer(s, secretgrpc.NewServer())
}

// lazyAuthn / lazyAuthz 把 bootstrap 包级桥接变量（InitBridges 在 appkit.BeforeStart
// 才赋值）适配为 authn/authz 接口，供 bundle 构造期注入——bundle 是构造期依赖注入，
// 而桥接是运行期装配，lazy 适配器衔接两者时序（请求期读取最新值）。
type lazyAuthn struct{}

func (lazyAuthn) Authenticate(ctx context.Context) (*authn.AuthClaims, error) {
	return bootstrappkg.Authenticator.Authenticate(ctx)
}

func (lazyAuthn) AuthenticateToken(token string) (*authn.AuthClaims, error) {
	return bootstrappkg.Authenticator.AuthenticateToken(token)
}

type lazyAuthz struct{}

func (lazyAuthz) Authorize(ctx context.Context, subject, object, action string) (bool, error) {
	return bootstrappkg.Authorizer.Authorize(ctx, subject, object, action)
}

func newGRPCServerOptions() []grpc.ServerOption {
	// M10.1（P10 验证）：gRPC 无公开方法（全部需认证），整条链切 bundle——
	// Error→RequestID→Observability→Authn→Audit→Authz 链序由 bundle 固化，
	// 替代此前手写的 7 段拦截器组装（authnInterceptor/authzInterceptor 闭包删除）。
	// P9 归一化经 bundle.Normalized() 内置于 Authz 与 Audit 两层。
	grpcBundle := bundle.New(
		bundle.Authn(lazyAuthn{}),
		bundle.Authz(lazyAuthz{}),
		bundle.Metrics(obmetrics.Recorder("bald/example")),
		bundle.Normalized(), // P9：FullMethod → 与 HTTP 同源的权限点
	)
	return grpcBundle.GRPCChain()
}

// buildServers 组装运行服务器集合：gRPC + HTTP，可选 grpc-gateway。
// gateway 仅在注入 gatewayFactory 时挂载（默认构建即挂载，由 init 注入）。
func buildServers(
	bootstrap *confv1.Bootstrap,
	httpSrv *server.HTTPServer,
	grpcSrv *server.GRPCServer,
	ready server.ReadinessFunc,
) []server.Server {
	servers := []server.Server{grpcSrv, httpSrv}
	if gatewayFactory == nil {
		return servers
	}
	// gateway 需连到 gRPC 服务（用其监听地址，须可连接，不能是 :0）。
	// gateway 配置在构造期即与 HTTP 同源绑定：地址走 gatewayAddr()，TLS 直接取主 HTTP 的 http.tls 段，
	// 不再依赖 BeforeStart 运行时回填（消除全局可变态 + 时序耦合）。
	gwHttpCfg := &confv1.Http{Addr: gatewayAddr(), Tls: bootstrap.GetHttp().GetTls()}
	gw, err := gatewayFactory(gwHttpCfg, bootstrap.GetGrpc(), ready)
	if err != nil {
		// 网关构造失败不应静默降级（否则 REST 路由凭空消失），直接 panic（fail-fast）。
		panic("build gateway server: " + err.Error())
	}
	return append(servers, gw)
}

// gatewayFactory 构造 grpc-gateway 服务器（REST → gRPC 转码）。M5 默认挂载：
// REST 请求经 registerGateway 转码进入 SecretService，复用同一 gRPC 拦截器链
// （认证/授权/多租户）。
var gatewayFactory = func(httpCfg *confv1.Http, grpcBackend *confv1.Grpc, ready server.ReadinessFunc) (*server.GatewayServer, error) {
	return server.NewGatewayServer(httpCfg, grpcBackend, registerGateway, ready)
}

// gatewayAddr 读取 gateway 监听地址：env BALD_GATEWAY_ADDR 优先，缺省 :8081，
// 与 HTTP 主服务（http.addr）分开避免端口冲突。
func gatewayAddr() string {
	if v := os.Getenv("BALD_GATEWAY_ADDR"); v != "" {
		return v
	}
	return ":8081"
}

// registerGateway 把 grpc-gateway 的 HTTP handler 注册到 runtime.ServeMux 并交回
// http.Handler（server.NewGatewayServer 依赖倒置，核心不依赖 grpc-gateway）。
// conn 由 GatewayServer 内部建立（指向本进程 gRPC 服务）。
func registerGateway(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
	mux := runtime.NewServeMux()
	if err := adminv1.RegisterSecretServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	return mux, nil
}

func setLogger(opts *baldlog.Options) {
	baldlog.SetLogger(baldlog.NewSlogLogger(opts,
		baldlog.WithFilter(baldlog.FilterKey("password")),
		baldlog.WithFilter(baldlog.FilterKey("token")),
		baldlog.WithAttrs(slog.String("service.name", "go-bald-admin")),
	))
	// M7 审计后端注入（落库版）在 InitBridges 之后装配（见 appkit.BeforeStart），
	// 因需 bootstrap.DB 已建立；此处仅设 Logger，不再提前注入 Auditor。
}
