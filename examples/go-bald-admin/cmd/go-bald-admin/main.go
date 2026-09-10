// Command go-bald-admin 是 go-bald-admin 服务入口：
// 用 bald appkit 组合一个 HTTP 服务与一个 gRPC 服务（双协议），
// 作为用 bald 重构 go-wind-admin/backend 的官方范例（验证 P0–P9）。
//
// 运行：
//
//	go run ./examples/go-bald-admin --config=examples/go-bald-admin/configs/go-bald-admin.yaml
//	go run ./examples/go-bald-admin --http.addr=:18080
//	GO_BALD_ADMIN_SERVER_HTTP_ADDR=:18080 go run ./examples/go-bald-admin
//	（env 前缀由 app name 规范化派生：go-bald-admin → GO_BALD_ADMIN_，下划线即点路径分隔）
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
	"google.golang.org/grpc"

	auditv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/audit/v1"
	dictv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/dict/v1"
	filev1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/file/v1"
	menuv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/menu/v1"
	permissionv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/permission/v1"
	adminv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/secret/v1"
	tenantv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/tenant/v1"
	userv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/user/v1"
	"github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver"
	secretgrpc "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/handler/grpc"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"

	obmetrics "github.com/kalandramo/bald-observability-otlp/metrics"
	obtrace "github.com/kalandramo/bald-observability-otlp/trace"
	nacoscontract "github.com/kalandramo/bald-registry-nacos/contract"
	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
	baldconfig "github.com/kalandramo/bald/bootstrap/config"
	securityaudit "github.com/kalandramo/bald/examples/go-bald-admin/internal/security/audit"
	baldlog "github.com/kalandramo/bald/log"
	baldlogadapter "github.com/kalandramo/bald/log/slog"
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	"github.com/kalandramo/bald/pkg/middleware/bundle"
	ginmw "github.com/kalandramo/bald/pkg/middleware/gin"
	"github.com/kalandramo/bald/transport"
	gateway "github.com/kalandramo/bald/transport/gateway"
	grpcserver "github.com/kalandramo/bald/transport/grpc"
	httpserver "github.com/kalandramo/bald/transport/http"
)

func serveRunE(_ *cobra.Command, _ []string) error {
	// 0. 框架级配置：proto 是唯一真相源，直接持有 Bootstrap 指针。
	bootstrap := bconf.NewBootstrap()
	bootstrap.GetServer().GetHttp().Addr = ":8080"

	// 日志系统接入（两阶段：先默认，配置加载后重建）。
	logOpts := baldlogadapter.NewOptions()
	setLogger(logOpts)

	// T9 可观测性契约化：OTLP trace + 指标双通道改由 bconf 契约 tracer/metrics 段驱动，
	// 装配挪至 BeforeStart（配置装载 Unmarshal 之后，见 setupObservability）。
	// Recorder/span 经 otel 全局 Provider lazy 解析——中间件先行构建不漏采：
	// Setup 设置全局 Provider 后新请求的埋点即走真后端，且 Servers 监听晚于
	// BeforeStart，无采样窗口损失。与 T7 registrar 同款「构造期 nil + 运行期接线」模式。
	obs := &observabilityWiring{}
	// C1：trace provider 注册为进程内组件（退出时由 appkit 统一逆序 Dispose flush
	// 尾批 span；obs.traceShutdown 在 BeforeStart 装配后才非 nil，未配置段时为 no-op）。
	traceComp := appkit.ComponentFunc("trace.provider", func(ctx context.Context) error {
		if obs.traceShutdown != nil {
			return obs.traceShutdown(ctx)
		}
		return nil
	})

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
	// T6：gin 审计中间件（写路径审计落库，与 gRPC AuditInterceptor 对称）。
	// wrap 型旁路：c.Next() 后记录，subject 由链内 AuthnMiddleware 注入后可读；
	// object/action 走 P9 归一化（path→"secret" 等，与 casbin 策略同源），
	// 审计后端经 audit.backends 热切换统一入口。
	router.Use(ginmw.AuditMiddleware(
		ginmw.AuditWithObjectResolver(authz.DefaultHTTPObject),
		ginmw.AuditWithActionResolver(authz.DefaultHTTPAction),
	))
	apiserver.RegisterRoutes(router, bizSet)                // gin handler 路由（T10：BizSet 直传）
	registerAdminRoutes(router, appRef, componentFactories) // M10.2 管理面（appRef 迟到绑定）
	httpSrv := httpserver.NewHTTPServer(bootstrap.GetServer().GetHttp(), router, ready)

	// T2：gRPC service 注册回调捕获 wire 装配的 biz（全部 service 需 biz 注入）。
	// 闭包取代原包级 var（bizSet 是局部变量）。secret 的 DeleteSecret 同经 biz
	// 真实删除（gRPC 直连与 gateway 转码共用），不再是占位返回。
	registerGRPC := func(s *grpc.Server) {
		adminv1.RegisterSecretServiceServer(s, secretgrpc.NewServer(bizSet.Secret))
		tenantv1.RegisterTenantServiceServer(s, secretgrpc.NewTenantServer(bizSet.Tenant))
		userv1.RegisterUserServiceServer(s, secretgrpc.NewUserServer(bizSet.User))
		menuv1.RegisterMenuServiceServer(s, secretgrpc.NewMenuServer(bizSet.Menu))
		permissionv1.RegisterPermissionServiceServer(s, secretgrpc.NewPermissionServer(bizSet.Permission))
		dictv1.RegisterDictTypeServiceServer(s, secretgrpc.NewDictTypeServer(bizSet.Dict))
		dictv1.RegisterDictEntryServiceServer(s, secretgrpc.NewDictEntryServer(bizSet.Dict))
		filev1.RegisterFileServiceServer(s, secretgrpc.NewFileServer(bizSet.File))        // T5
		auditv1.RegisterAuditServiceServer(s, secretgrpc.NewAuditServer(bizSet.AuditLog)) // T6
	}
	grpcSrv := grpcserver.NewGRPCServerWithRegister(
		bootstrap.GetServer().GetGrpc(),
		newGRPCServerOptions(),
		registerGRPC,
		ready,
	)

	// 2. 组装 AppKit（含可选 grpc-gateway，见 buildServers）。
	app := newApp(bootstrap, logOpts, httpSrv, grpcSrv, ready, traceComp, bizSet, obs)
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
	bootstrap *bootstrapv1.BootstrapConfig,
	logOpts *baldlogadapter.Options,
	httpSrv *httpserver.HTTPServer,
	grpcSrv *grpcserver.GRPCServer,
	ready transport.ReadinessFunc,
	traceComp appkit.Component,
	bizSet *apiserver.BizSet,
	obs *observabilityWiring,
) *appkit.AppKit {
	var app *appkit.AppKit
	// T7：注册中心契约装配（New 构造路径等价于 FromBootstrap 的 buildRegistrar）：
	// 显式注册表声明可用后端（未 import 的后端零依赖），BeforeStart 配置装载后
	// 按 registry 段构造真实 registrar；cleanup 挂停机 Effect（Deregister 先于
	// Effect 回放，顺序安全）。registry 段不支持热更新（client 重建侵入性大）。
	regRegistry := registrarRegistry()
	var regCleanup func()
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

		// T7：nacos client 关闭效应（BeforeStart 契约装配时经 regCleanup 激活，
		// 未配置 registry 段则为空操作）。
		appkit.Effect("appkit:registrar-client", func(context.Context) error {
			if regCleanup != nil {
				regCleanup()
			}
			return nil
		}),

		// T9：metrics 暴露端点优雅关闭（BeforeStart setupObservability 装配后激活；
		// Shutdown 让 in-flight 抓取完成，避免连接被硬切）。
		appkit.Effect("metrics:server", func(ctx context.Context) error {
			if obs.metricsSrv != nil {
				return obs.metricsSrv.Shutdown(ctx)
			}
			return nil
		}),

		// 业务 flag 接入：前缀与配置键一致（--server.http.addr ⇔ server.http.addr ⇔ GO_BALD_ADMIN_SERVER_HTTP_ADDR）。
		appkit.Bind("server.http", bootstrap.GetServer().GetHttp()),
		appkit.Bind("server.grpc", bootstrap.GetServer().GetGrpc()),
		appkit.Bind("", logOpts),

		appkit.OnConfigChange(func(m map[string]any) {
			logger := baldlog.GetLogger()
			logger.Info(context.Background(), "config changed",
				"server.http.addr", app.Config().GetString("server.http.addr"),
				"server.grpc.addr", app.Config().GetString("server.grpc.addr"))
			if err := baldconfig.Unmarshal(m, bootstrap); err != nil {
				logger.Error(context.Background(), "reload config failed", "error", err)
			}
		}),

		// R1 增量协调（key 级订阅）：仅当 http.addr 实际变化才触发（同值刷新、
		// 其他 key 变更均不波及），与全量 reload 互补——全量做 Unmarshal 重建，
		// key 级做定点观测/定点重载。
		appkit.OnKeyChange("server.http.addr", func(old, new string) {
			baldlog.GetLogger().Info(context.Background(), "server.http.addr changed",
				"old", old, "new", new)
		}),

		appkit.Servers(buildServers(bootstrap, httpSrv, grpcSrv, ready)...),

		appkit.BeforeStart(func(ctx context.Context) error {
			m := app.Settings()
			if m == nil {
				return nil
			}
			if err := baldconfig.Unmarshal(m, bootstrap); err != nil {
				return fmt.Errorf("unmarshal config: %w", err)
			}
			if err := bconf.Validate(bootstrap); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}
			// 阶段 B：按最终配置重建 Logger，并装配 bald 桥接（P7/P8/P9 注册点）。
			setLogger(baldbootstrap.LogOptions(bootstrap.GetLogger()))
			// T9：契约驱动可观测性（tracer / metrics 段）——trace OTLP 直推 +
			// 指标双通道（Prometheus 暴露端点 + 可选 OTLP 直推）。须在 Servers
			// 监听前（BeforeStart 语义保证），段不支持热更新（exporter/Provider
			// 重建侵入大，与 registry 段同款决策）。
			if err := setupObservability(bootstrap, obs); err != nil {
				return fmt.Errorf("setup observability: %w", err)
			}
			// T0：注入真实依赖配置（database.sql / cache.redis / storage.minio 段 +
			// 业务自持 file.bucket），openDB/Redis/MinIO 构造据此分流；须在 InitBridges 之前。
			bootstrappkg.Configure(bootstrap, app.Config().GetString("file.bucket"))
			// 在 bootstrap 包内装配 bald 桥接（P7/P8/P9 注册点）：M1+ 注入
			// Authenticator / Authorizer / store.RegisterTenant / store.RegisterDataScope。
			if err := bootstrappkg.InitBridges(ctx); err != nil {
				return fmt.Errorf("init bridges: %w", err)
			}
			// T8：文件存储运行期接线——InitializeBiz 构造期值拷贝 bootstrap.MinioStorage
			// 拿到 nil（InitBridges 尚未执行，§9 真调暴露的 e2e 盲区），桥接装配后补注。
			bizSet.File.SetStorage(bootstrappkg.MinioStorage, bootstrappkg.FileBucket)
			// 同款时序：secret/dict 的 Cache-Aside 接入配置驱动的 RedisCache——
			// cache.redis 段（含 password/db）只流向 bootstrap.RedisCache，wire 的
			// env 通道（BALD_ADMIN_REDIS_ADDR）拿不到完整参数、对带密码实例 ping
			// 即失败；不接线则配置驱动运行下缓存静默失效。未配置段时 RedisCache
			// 为 nil，SetCache 不覆盖，保留 wire env 通道（CI 覆盖手段）。
			if bootstrappkg.RedisCache != nil {
				bizSet.Secret.SetCache(bootstrappkg.RedisCache)
				bizSet.Dict.SetCache(bootstrappkg.RedisCache)
			}
			// T7：契约驱动注册中心——registry 段为真相源，type 为空/未注册均
			// fail-fast（与 FromBootstrap 的 buildRegistrar 同语义），Nacos 不可达
			// 在此显式报错而非静默降级。注意 cleanup 必须 `=` 赋外层变量，
			// 闭包内 `:=` 会遮蔽导致停机 Effect 拿 nil。
			reg, cleanup, err := regRegistry.Build(ctx, bootstrap.GetRegistry())
			if err != nil {
				return fmt.Errorf("build registrar: %w", err)
			}
			app.SetRegistrar(reg)
			regCleanup = cleanup
			// 审计后端注入（R1-2 期望态协调）改由 appkit.Reconcile 驱动：声明期望集合
			// （audit.backends）后，框架在启动收敛期与每次配置变更时自动 diff-apply，
			// 此处不再静态装配——单一事实来源收敛到配置键，避免"配置与代码两处声明"。
			return nil
		}),
		// R1-2 期望态协调：以 audit.backends 为期望态，与当前生效后端集合 diff，
		// 自动重建 MultiAuditor 收敛（幂等）。首次收敛在 AfterStart 前（runReconcilers
		// 于基线 loadConfig 后、BeforeStart 链之后触发）；后续 OnConfigChange 携带新
		// 快照再次触发，实现运行期热切换而无需重启。
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

// observabilityWiring 承载 BeforeStart 装配、停机期消费的可观测性句柄
// （构造期 nil + 运行期接线，T7 regCleanup / T8 SetStorage 同模式）。
type observabilityWiring struct {
	traceShutdown func(context.Context) error
	metricsSrv    *http.Server
}

// setupObservability 按 bconf 契约 tracer/metrics 段装配可观测性（显式主开关契约）：
//   - 段缺省（yaml 无 tracer:/metrics: 段）→ 默认行为：no-op trace + 仅 Prometheus
//     暴露（addr 缺省 :9091，T8 根治与 gRPC 错峰）——零配置可运行，冒烟/CI 不破；
//   - 段存在 → type 必须显式声明，空串/未知值启动期报错（行为由声明决定，
//     不靠 endpoint 非空反推意图）：
//       tracer.type  仅支持 "otlp"（endpoint 必填）；
//       metrics.type "prometheus"=仅本地暴露 / "otlp"=暴露+OTLP 直推双通道
//                    （endpoint 必填）；type=prometheus 而 otlp endpoint（含 env）
//                    非空 → 矛盾配置报错。
//
// env 覆盖通道（优先级 env > yaml，与 flag>env>本地文件一致）：BALD_ADMIN_OTLP_ADDR
// 覆盖双通道 endpoint、BALD_ADMIN_METRICS_ADDR 覆盖暴露端口——只提供地址，不改变
// type 声明的语义（配 prometheus 不会因 env 翻转成推送）。
func setupObservability(bootstrap *bootstrapv1.BootstrapConfig, obs *observabilityWiring) error {
	// ---- trace：契约 tracer 段（缺省=no-op；存在则 type 必须显式 "otlp"）----
	tr := bootstrap.GetTracer()
	totlp := tr.GetOtlp()
	traceEndpoint := ""
	if tr != nil {
		switch tr.GetType() {
		case "otlp":
			traceEndpoint = totlp.GetEndpoint()
			if v := os.Getenv("BALD_ADMIN_OTLP_ADDR"); v != "" {
				traceEndpoint = v
			}
			if traceEndpoint == "" {
				return fmt.Errorf("tracer.type=otlp requires tracer.otlp.endpoint (or env BALD_ADMIN_OTLP_ADDR)")
			}
		case "":
			return fmt.Errorf("tracer.type is required when tracer section is present (expect \"otlp\")")
		default:
			return fmt.Errorf("tracer.type %q unsupported (expect \"otlp\")", tr.GetType())
		}
	}
	traceShutdown, err := obtrace.Setup(
		obtrace.WithOTLPAddr(traceEndpoint),
		obtrace.WithServiceName("go-bald-admin"),
		obtrace.WithInsecure(totlp.GetInsecure()),
		obtrace.WithHeaders(totlp.GetHeaders()),
		obtrace.WithSampler(totlp.GetSampler()),
		obtrace.WithSampleRatio(totlp.GetSampleRatio()),
	)
	if err != nil {
		return fmt.Errorf("setup trace: %w", err)
	}
	obs.traceShutdown = traceShutdown // `=` 赋值（T7 教训：闭包内 `:=` 遮蔽致停机拿 nil）

	// ---- metrics：契约 metrics 段（缺省=仅暴露；存在则 type 必须显式）----
	m := bootstrap.GetMetrics()
	prom := m.GetPrometheus()
	motlp := m.GetOtlp()
	metricsAddr := prom.GetAddr()
	if metricsAddr == "" {
		metricsAddr = ":9091" // T8 根治缺省：与 gRPC(:9090) 错开
	}
	if v := os.Getenv("BALD_ADMIN_METRICS_ADDR"); v != "" {
		metricsAddr = v
	}
	metricsPath := prom.GetPath()
	if metricsPath == "" {
		metricsPath = "/metrics"
	}
	metricsEndpoint := ""
	if m != nil {
		switch m.GetType() {
		case "otlp":
			metricsEndpoint = motlp.GetEndpoint()
			if v := os.Getenv("BALD_ADMIN_OTLP_ADDR"); v != "" {
				metricsEndpoint = v
			}
			if metricsEndpoint == "" {
				return fmt.Errorf("metrics.type=otlp requires metrics.otlp.endpoint (or env BALD_ADMIN_OTLP_ADDR)")
			}
		case "prometheus":
			if motlp.GetEndpoint() != "" || os.Getenv("BALD_ADMIN_OTLP_ADDR") != "" {
				return fmt.Errorf("metrics.type=prometheus conflicts with otlp endpoint configured (use type \"otlp\" to enable push)")
			}
		case "":
			return fmt.Errorf("metrics.type is required when metrics section is present (expect \"prometheus\" or \"otlp\")")
		default:
			return fmt.Errorf("metrics.type %q unsupported (expect \"prometheus\" or \"otlp\")", m.GetType())
		}
	}
	metricsHandler, err := obmetrics.Setup(
		obmetrics.WithOTLPAddr(metricsEndpoint),
		obmetrics.WithServiceName("go-bald-admin"),
		obmetrics.WithInsecure(motlp.GetInsecure()),
		obmetrics.WithHeaders(motlp.GetHeaders()),
		obmetrics.WithInterval(time.Duration(motlp.GetPushInterval())*time.Second),
	)
	if err != nil {
		return fmt.Errorf("setup metrics: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle(metricsPath, metricsHandler)
	srv := &http.Server{Addr: metricsAddr, Handler: mux}
	go func() {
		if serveErr := srv.ListenAndServe(); serveErr != nil && serveErr != http.ErrServerClosed {
			baldlog.GetLogger().Error(context.Background(), "metrics server stopped", "error", serveErr.Error())
		}
	}()
	obs.metricsSrv = srv
	return nil
}

// registrarRegistry 显式注册注册中心契约 provider（业务按需 import 各后端
// contract 包，未 import 的后端零依赖；registry.type 指向未注册后端时 Build
// fail-fast，而不是静默无注册）。
func registrarRegistry() *appkit.RegistrarRegistry {
	rr := appkit.NewRegistrarRegistry()
	rr.MustRegister(nacoscontract.Type, nacoscontract.Provider)
	return rr
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
// MultiAuditor，Unmount 即注销并收尾后端自身生命周期。其名 == 后端名
// （log/store/stream），正是协调器用于 diff 的标识，与 ReconcileCtx.Mounted() 一一对应。
type auditBackendComponent struct {
	name  string
	build func() audit.Auditor // 延迟构造：依赖 InitBridges 后的 DB/Redis
	aud   audit.Auditor        // Start 期构造的实例，Dispose 时按需收尾
}

func (c *auditBackendComponent) Name() string { return c.name }

func (c *auditBackendComponent) Start(ctx context.Context) error {
	a := c.build()
	if a == nil {
		return fmt.Errorf("audit backend %q unavailable", c.name)
	}
	c.aud = a
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
	// 后端自身生命周期收尾：StreamAuditor 停后台 goroutine 并 drain 剩余缓冲
	// （Mount/Unmount 由 ReconcileCtx 串行化，无并发写 c.aud；log/store 无
	// Close 方法则零副作用）。否则 audit.backends 热切换每次泄漏一个 goroutine
	// + 1024 容量 chan，且已入队事件被静默丢弃。
	if c.aud != nil {
		if closer, ok := c.aud.(interface{ Close() }); ok {
			closer.Close()
		}
	}
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
// 每次 OnConfigChange 携带新快照再次调用，实现运行期热切换（如 [store]→[store,stream]）。
func reconcileAudit(ctx context.Context, rctx *appkit.ReconcileCtx) error {
	want := parseAuditBackends(rctx.String("audit.backends"))
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
	bootstrap *bootstrapv1.BootstrapConfig,
	httpSrv *httpserver.HTTPServer,
	grpcSrv *grpcserver.GRPCServer,
	ready transport.ReadinessFunc,
) []transport.Server {
	servers := []transport.Server{grpcSrv, httpSrv}
	if gatewayFactory == nil {
		return servers
	}
	// gateway 需连到 gRPC 服务（用其监听地址，须可连接，不能是 :0）。
	// gateway 配置在构造期即与 HTTP 同源绑定：地址走 gatewayAddr()，TLS 直接取主 HTTP 的 http.tls 段，
	// 不再依赖 BeforeStart 运行时回填（消除全局可变态 + 时序耦合）。
	gwHttpCfg := &bootstrapv1.Server_Http{Addr: gatewayAddr()}
	if t := bootstrap.GetServer().GetHttp(); t.GetTls() != nil {
		gwHttpCfg.Tls = t.GetTls()
	}
	gw, err := gatewayFactory(gwHttpCfg, bootstrap.GetServer().GetGrpc(), ready)
	if err != nil {
		// 网关构造失败不应静默降级（否则 REST 路由凭空消失），直接 panic（fail-fast）。
		panic("build gateway server: " + err.Error())
	}
	return append(servers, gw)
}

// gatewayFactory 构造 grpc-gateway 服务器（REST → gRPC 转码）。M5 默认挂载：
// REST 请求经 registerGateway 转码进入 SecretService，复用同一 gRPC 拦截器链
// （认证/授权/多租户）。

var gatewayFactory = func(httpCfg *bootstrapv1.Server_Http, grpcBackend *bootstrapv1.Server_Grpc, ready transport.ReadinessFunc) (*gateway.GatewayServer, error) {
	return gateway.NewGatewayServer(httpCfg, grpcBackend, registerGateway, ready)
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
// http.Handler（transport.NewGatewayServer 依赖倒置，核心不依赖 grpc-gateway）。
// conn 由 GatewayServer 内部建立（指向本进程 gRPC 服务）。T2 起挂载 tenant/user 网关。
func registerGateway(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
	mux := runtime.NewServeMux()
	if err := adminv1.RegisterSecretServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	if err := tenantv1.RegisterTenantServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	if err := userv1.RegisterUserServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	if err := menuv1.RegisterMenuServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	if err := permissionv1.RegisterPermissionServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	if err := dictv1.RegisterDictTypeServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	if err := dictv1.RegisterDictEntryServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	if err := filev1.RegisterFileServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	if err := auditv1.RegisterAuditServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	return mux, nil
}

func setLogger(opts *baldlogadapter.Options) {
	baldlog.SetLogger(baldlogadapter.NewSlogLogger(opts,
		baldlogadapter.WithFilter(baldlogadapter.FilterKey("password")),
		baldlogadapter.WithFilter(baldlogadapter.FilterKey("token")),
		baldlogadapter.WithAttrs(slog.String("service.name", "go-bald-admin")),
	))
	// M7 审计后端注入（落库版）在 InitBridges 之后装配（见 appkit.BeforeStart），
	// 因需 bootstrap.DB 已建立；此处仅设 Logger，不再提前注入 Auditor。
}
