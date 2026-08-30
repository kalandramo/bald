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
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"google.golang.org/grpc"

	"github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver"
	secretgrpc "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/grpc"
	adminv1 "github.com/kalandramo/bald/examples/go-bald-admin/gen/secretv1"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"

	"github.com/kalandramo/bald/pkg/appkit"
	baldconf "github.com/kalandramo/bald/pkg/conf"
	confv1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/config/v1"
	baldconfig "github.com/kalandramo/bald/pkg/config"
	baldlog "github.com/kalandramo/bald/pkg/log"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	grpcmw "github.com/kalandramo/bald/pkg/middleware/grpc"
	"github.com/kalandramo/bald/pkg/authz"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/registry/inmemory"
	"github.com/kalandramo/bald/pkg/server"
	securityaudit "github.com/kalandramo/bald/examples/go-bald-admin/internal/security/audit"
	obmetrics "github.com/kalandramo/bald/examples/go-bald-admin/internal/observability/metrics"
	obtrace "github.com/kalandramo/bald/examples/go-bald-admin/internal/observability/trace"
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
	// 两者均按 BALD_ADMIN_OTLP_ADDR 开关；须在拦截器构建前 Setup，使埋点接入 exporter。
	traceShutdown, err := obtrace.Setup()
	if err != nil {
		return fmt.Errorf("setup trace: %w", err)
	}
	traceShutdownFn = traceShutdown // 供 BeforeStop flush（进程退出前导出缓冲 span）

	metricsAddr := os.Getenv("BALD_ADMIN_METRICS_ADDR")
	if metricsAddr == "" {
		metricsAddr = ":9090"
	}
	metricsHandler, err := obmetrics.Setup()
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
	router := gin.New()
	router.Use(mid.Recovery(), mid.RequestID(), mid.Logging())
	// M7 审计：旁路记录 REST 访问（subject/object/action/result）。复用 P9 归一化原语，
	// 与 gRPC 同源；置于业务路由之前，c.Next 后记录最终响应状态。
	// M8 指标：审计同源 emit 指标。
	router.Use(mid.AuditMiddleware(nil,
		mid.AuditWithObjectResolver(authz.DefaultHTTPObject),
		mid.AuditWithActionResolver(authz.DefaultHTTPAction),
		mid.AuditWithMetrics(obmetrics.Recorder()),
	))
	apiserver.RegisterRoutes(router, bizSet.Auth, bizSet.Secret) // gin handler 路由
	httpSrv := server.NewHTTPServer(bootstrap.GetHttp(), router, ready)

	grpcSrv := server.NewGRPCServerWithRegister(
		bootstrap.GetGrpc(),
		newGRPCServerOptions(),
		registerGRPCService,
		ready,
	)

	// 2. 组装 AppKit（含可选 grpc-gateway，见 buildServers）。
	app := newApp(bootstrap, logOpts, httpSrv, grpcSrv, ready)

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
) *appkit.AppKit {
	var app *appkit.AppKit
	app = appkit.New(
		appkit.Name("go-bald-admin"),
		appkit.Version("v0.1.0"),
		appkit.StopTimeout(15*time.Second),

		appkit.ConfigFile("configs/go-bald-admin.yaml"),
		appkit.WatchConfigFile(true),

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
			// M7 审计后端注入（组合器，多后端逐一落库/入流，任一失败仅降级不阻断）。
			// 须置于 InitBridges 之后：DB 已建立并迁移审计表；Redis 可选（无则仅落库+日志）。
			auditors := []audit.Auditor{securityaudit.NewStore(bootstrappkg.DB)}
			if bootstrappkg.RedisCache != nil && bootstrappkg.RedisCache.Client() != nil {
				auditors = append(auditors, securityaudit.NewStream(bootstrappkg.RedisCache.Client()))
			}
			audit.SetAuditor(securityaudit.NewMulti(auditors...))
			return nil
		}),
		appkit.AfterStart(func(ctx context.Context) error {
			ctx = baldlog.ContextWithAttrs(ctx,
				slog.String("stage", "started"), slog.String("grpc", grpcSrv.Endpoint()))
			baldlog.GetLogger().Info(ctx, "go-bald-admin started", "http", httpSrv.Endpoint())
			return nil
		}),
		appkit.BeforeStop(func(ctx context.Context) error {
			baldlog.GetLogger().Info(ctx, "go-bald-admin stopping")
			if traceShutdownFn != nil {
				if shutErr := traceShutdownFn(ctx); shutErr != nil {
					baldlog.GetLogger().Warn(ctx, "trace provider shutdown", "error", shutErr.Error())
				}
			}
			return nil
		}),
	)
	return app
}

var osExit = func(code int) { os.Exit(code) }

// traceShutdownFn 是 obtrace.Setup 返回的全局 TracerProvider shutdown（flush 缓冲 span），
// 在 appkit.BeforeStop 调用，确保进程退出前导出未发送的 trace。未开 OTLP 时为 no-op。
var traceShutdownFn func(context.Context) error

// registerGRPCService 是 gRPC service 注册回调（M5 用 proto 生成的 SecretServiceServer）。
var registerGRPCService = func(s *grpc.Server) {
	adminv1.RegisterSecretServiceServer(s, secretgrpc.NewServer())
}

func newGRPCServerOptions() []grpc.ServerOption {
	// Authn/Authz 拦截器延迟读取 bootstrap 包级变量：grpc server 在 main 构造时
	// Authenticator 尚为 nil（InitBridges 在 appkit.BeforeStart 才赋值），但拦截器
	// 在请求期执行，此时 bridge 已就绪，闭包读取最新值即可。
	authnInterceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return grpcmw.AuthnInterceptor(bootstrappkg.Authenticator)(ctx, req, info, handler)
	}
	authzInterceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// P9 反哺：gRPC 侧经核心归一化把 FullMethod 翻译为与 HTTP 同源的权限点
		// （object=secret/auth 资源名、action=get/delete/list/write），casbin 桥接不再重复归一化。
		return grpcmw.AuthzInterceptor(bootstrappkg.Authorizer,
			grpcmw.WithObjectResolver(authz.DefaultGRPCObject),
			grpcmw.WithActionResolver(authz.DefaultGRPCAction),
		)(ctx, req, info, handler)
	}
	unaryInterceptors := []grpc.UnaryServerInterceptor{
		grpcmw.ErrorInterceptor(),
		grpcmw.RequestIDInterceptor(),
		grpcmw.UnaryObservability(),
		authnInterceptor, // 认证：无 token / 伪造 token -> Unauthenticated
		// M7 审计：置于 Authn 内侧（可读 subject/tenant）、Authz 外侧（捕获最终 result），
		// 旁路不阻断业务。复用 P9 归一化原语，与 REST 同源。
		// M8 指标：审计同源 emit 指标（bald_requests_total/bald_request_duration_seconds）。
		grpcmw.AuditInterceptor(nil,
			grpcmw.AuditWithObjectResolver(authz.DefaultGRPCObject),
			grpcmw.AuditWithActionResolver(authz.DefaultGRPCAction),
			grpcmw.AuditWithMetrics(obmetrics.Recorder()),
		),
		authzInterceptor, // 授权：角色无权限 -> PermissionDenied
	}
	return []grpc.ServerOption{
		// M5 起用标准 proto codec（grpc-gateway 以 protobuf 转发到本后端，需一致）。
		grpc.ChainUnaryInterceptor(unaryInterceptors...),
	}
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
