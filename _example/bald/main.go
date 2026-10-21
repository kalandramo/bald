// Command bald 是 bald 服务框架的示例入口：
// 使用 appkit.MustServe（单一装配入口）约定装配一个 HTTP 服务与一个 gRPC 服务
// ——契约/flag/env/配置文件驱动一切可配置项，代码只声明配置表达不了的业务能力。
//
// 装配分工（配置驱动参数，代码声明能力）：
//
//	框架内化（Serve + std.Standard()）：--config 解析、契约装载、Bind×3、
//	配置装载+校验、日志两阶段（启动默认→契约重建）、热更新回调、app 元数据
//	（id/name/version/env/stop_timeout）、服务器构造（std server Provider，
//	能力声明驱动）、注册中心启停、停机资源释放。
//	第三方 Provider（加法式）：storegorm.Provider 消费契约 database.sql 段
//	（目标 main.go 形态的首个第三方后端范本，见 pkg/appkit/std 包文档）；
//	loggerProvider 覆盖 std 默认日志（Attr 层脱敏 password/token + service.name）。
//	业务域（一等概念，判据 ① 活体）：greetv1.Domain 经域清单（domains.gen.go）
//	变参直传——域自我接线（读自己的配置段、构造能力、持有端口）+ 自持路由与
//	gRPC service 注册；新增一个业务域只手写 3 个文件、装配零改动。
//	代码声明（业务必供）：gin 路由（ping/users/articles 演示）、拦截器链序、
//	就绪探针依赖、gateway 转码注册（WithGatewayRegister，-tags grpcgw）。
//
// 运行：
//
//	# 本地文件 + 环境变量 + flag（无需远程配置中心即可运行）
//	# 配置文件随示例自带：_example/bald/configs/bald-demo.yaml（路径相对运行目录）
//	cd _example/bald && go run .                    # 自动加载 configs/bald-demo.yaml
//	BALD_DEMO_HTTP_ADDR=:18080 go run ./_example/bald        # 环境变量覆盖 http.addr
//	go run ./_example/bald --http.addr=:18080                # flag 优先级最高
//
//	# 多环境（本地按 bald-demo-prod.yaml 选择默认文件）
//	go run ./_example/bald --env=prod
//
//	# 切换日志格式 / 级别（--log.* 由 pkg/log 提供，热更新即改即生效）
//	go run ./_example/bald --log.format=json --log.level=debug
//
//	# 验证 HTTP 示例路由（httpserver.NewHTTPServer 挂载 gin.Engine）：
//	curl -i http://127.0.0.1:8080/v1/ping
//	curl -i -XPOST http://127.0.0.1:8080/v1/greet -d '{"name":"bald"}'
//	curl -i -XPOST 'http://127.0.0.1:8080/v1/articles/42?lang=zh' -d '{"title":"hi"}'
//
//	# grpc-gateway transcoding 示例（需本地 protoc 工具链生成代码后启用）：
//	#   见 docs/devel/zh-CN/grpc-gateway 配置与 transcoding.md
//	#   生成：cd _example/bald && make proto && cd ../..
//	#   运行：cd _example/bald && go run -tags grpcgw .
//	#   验证：curl -i -XPOST http://127.0.0.1:8080/v1/greet -d '{"name":"bald"}'
//	#   （grpcgw 构建下契约 server.http.driver=grpc-gateway，网关转码面占用
//	#   server.http 端口，gin 演示路由让位——同一端口只跑一个面）
//
//	# Windows (PowerShell)：用 --% 关闭 PowerShell 解析，JSON 内双引号以 \ 转义
//	curl.exe -i http://127.0.0.1:8080/v1/ping
//	curl.exe --% -i -XPOST http://127.0.0.1:8080/v1/greet -H "Content-Type: application/json" -d "{\"name\":\"bald\"}"
//	curl.exe --% -XPOST "http://127.0.0.1:8080/v1/articles/42?lang=zh" -H "Content-Type: application/json" -d "{\"title\":\"hi\"}"
//
//	# 远程配置中心（etcd/nacos）：见 register_nacos.go（build tag nacos）
//	go run ./_example/bald   # 远程作基准，本地覆盖（配置随示例自带，自动加载）
package main

import (
	"context"
	"fmt"
	"log/slog" // 仅用于 ContextWithAttrs 的 slog.Attr 构造（如 log.String）。
	"net/http"
	"os"
	"os/exec"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	greetv1 "github.com/kalandramo/bald/example/bald/greetv1"
	usercmd "github.com/kalandramo/bald/example/bald/user"

	"github.com/kalandramo/bald/berrors"
	storegorm "github.com/kalandramo/bald/contrib/store-gorm"
	"github.com/kalandramo/bald/health"
	baldlog "github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/appkit/std"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	grpcmw "github.com/kalandramo/bald/pkg/middleware/grpc"
	"github.com/kalandramo/bald/registry/inmemory"
	"github.com/kalandramo/bald/transport/web"
)

func serveRunE(_ *cobra.Command, _ []string) error {
	// 0. 业务能力（配置文件表达不了，只能代码声明）。
	//    健康检查：一个聚合器同时驱动 HTTP /healthz /readyz 与 gRPC health
	//    （未就绪 → /readyz 503、gRPC NOT_SERVING，K8s 摘流量）。
	healthChecker := health.New()
	healthChecker.Register("demo", health.PingFunc(func(ctx context.Context) error {
		// TODO: 在此检查业务依赖（如 DB ping、下游连通性）。返回 nil=就绪。
		return nil
	}))

	// 1. 单一装配入口：契约装载、约定装配（Bind×3、装载/校验/日志两阶段、
	//    热更新、服务器构造全部由框架内化）、信号处理与 Run 全部在 Serve 内部
	//    （见 newAppOptions 的能力声明）。失败记日志后以非零码退出。
	appkit.MustServe(context.Background(), newAppOptions(healthChecker)...)
	return nil
}

// main 是进程入口：构造 cobra root 命令，默认子命令即原 serve 行为；
// 挂载 `user`（存储后端切换演示）子命令；未知子命令走 kubectl 风格
// PATH 插件发现（bald-<name> 可执行文件）。代码生成脚手架已提升为核心
// cmd/bald（`go install github.com/kalandramo/bald/cmd/bald`），见 internal/codegen。
func main() {
	root := &cobra.Command{
		Use:   "bald",
		Short: "bald 服务框架示例",
		RunE:  serveRunE,
	}
	root.AddCommand(usercmd.NewUserCommand())

	// kubectl 风格插件发现：若首参不是已知子命令，转发到 PATH 中的 bald-<name>。
	if len(os.Args) > 1 {
		sub := os.Args[1]
		if !isKnownCommand(root, sub) {
			plugin := "bald-" + sub
			path, err := exec.LookPath(plugin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "unknown command %q (and no plugin %q found in PATH)\n", sub, plugin)
				if err := root.Execute(); err != nil { // 触发 cobra 原生 unknown 提示
					osExit(1)
				}
				return
			}
			cmd := exec.Command(path, os.Args[2:]...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
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

// isKnownCommand 判断 name 是否 root 的已注册子命令（或其 persistent 别名）。
func isKnownCommand(root *cobra.Command, name string) bool {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return true
		}
	}
	return false
}

// newAppOptions 声明业务能力（Serve 的约定装配消费这些 Option）。
//
// 抽成函数是为了让 e2e 测试能复用**同一份**声明（greet_e2e_test.go），
// 而不是在测试里另抄一份——复制出来的应用与真实运行的不一致，测试就失去回归价值。
//
// Serve 内化的约定（原本要手写的样板）：
//   - 契约装载：bconf.NewBootstrap 骨架 + --config（默认 configs/bald-demo.yaml，
//     来自 ConfigFile）的文件/env 基线预装载；
//   - Bind server.http/server.grpc/--log.* 三组 flag（四源路径统一）；
//   - BeforeStart：Settings→Unmarshal→Validate→按契约 logger 段重建 Logger；
//   - OnConfigChange：热更新副本试装载+校验后原子落盘，并重建 Logger；
//   - app 元数据（name/version/env/stop_timeout）取自契约 app 段（本示例由
//     configs/bald-demo.yaml 的 app 段供给）；
//   - 停机恢复启动前 Logger、释放配置层资源（Effect 效应账本）。
//
// 业务保留的声明（配置驱动不了）：路由、gRPC service、拦截器链序、
// 探针依赖、日志脱敏——见下方各 Option。
//
// HTTP 面按构建分叉（同一 server.http 段只跑一个面）：默认构建走 gin 演示
// 路由；grpcgw 构建走网关转码面（WithGatewayRegister + 契约
// server.http.driver=grpc-gateway，见 register_grpcgw.go）。
func newAppOptions(healthChecker *health.Health) []appkit.Option {
	opts := []appkit.Option{
		// --- 框架默认后端 + 第三方 Provider ---
		// std.Standard()：框架自有后端的段驱动 Provider 集合（http/grpc 服务器
		// 构造、audit.log 默认——对账表见 pkg/appkit/std 包文档）。
		std.Standard(),
		// storegorm.AppProvider()：第三方后端加法式范本——消费契约 database.sql
		// 段（configs/bald-demo.yaml 的 database 段驱动），产物经
		// app.Resource("database.sql") 取用，停机自动释放连接池。
		storegorm.AppProvider(),

		// 配置命名空间由契约 app.name 派生（B6 两遍合并）：configs/bald-demo.yaml 的
		// app.name: "bald-demo" 即 env 前缀（BALD_DEMO_*）与多环境文件名来源——
		// 契约无 app.name 时启动 fail-fast 且报错可行动。
		// --- 能力声明（代码提供） ---
		// gRPC：拦截器链（链序说明见 newGRPCServerOptions，ErrorInterceptor
		// 必须最外层；与 e2e 复用同一构造，杜绝「测试与生产不一致」）。
		// service 注册不再在此声明——greet 域经 Domain.Services 自持注册
		//（引擎并入 server 构建的注册回调，业务注册在前、域按声明序在后）。
		appkit.WithGRPCOptions(newGRPCServerOptions),

		// 健康检查默认装配：HTTP 双探针 + gRPC health 状态对称联动。
		appkit.WithHealth(healthChecker),

		appkit.AfterStart(func(ctx context.Context) error {
			ctx = baldlog.ContextWithAttrs(ctx, slog.String("stage", "started"))
			// 端点地址由框架的注册日志打印（Registrar 生效时 Run 会输出
			// "appkit registered" + endpoints，地址为契约最终值）——此处不再
			// 重复读契约（契约已是 Serve 内部产物，业务不持引用）。
			baldlog.Info(ctx, "bald-demo started")
			return nil
		}),

		// --- 配置驱动参数（也可全写在 configs/bald-demo.yaml 里） ---
		// 服务注册中心：默认构建演示用 inmemory（零外部依赖、覆盖
		// register→运行→deregister 全流程）；显式实例优先于契约 registry 段。
		// 生产：-tags nacos（或 etcd/consul）走段驱动 Provider 装配——yaml
		// registry 段驱动 + registry 段 Provider（见 register_nacos.go 的
		// nacosRegistrarProvider）。
		appkit.Registrar(inmemory.New()),
		// 本地配置文件 + fsnotify 热更新（缺失不报错，可只用 env/flag/远程）。
		// 多环境：契约 app.env（BALD_DEMO_APP_ENV）选 {Name}-{Env}.yaml。
		appkit.ConfigFile("configs/bald-demo.yaml"),
		appkit.WatchConfigFile(true),

		// 日志装饰（B7 后装饰归 provider 自持）：本应用的 loggerProvider 覆盖
		// std 默认（段路径末者胜），恢复 Attr 层脱敏演示——password/token
		// 掩码为 *** + service.name 固定属性（见 providers.go 与其行为测试）。
		newLoggerProvider(),
	}
	// 业务域（一等概念）：域清单（domains.gen.go——bald gen domain 生成）
	// 变参直传。新增域只改清单文件，
	// 装配零改动。
	opts = append(opts, domainOptions...)
	// HTTP 面二选一（同一 server.http 段只跑一个面，语义见 WithGatewayRegister）：
	//   - 默认构建：gin 演示路由（gin.Engine 即 http.Handler），中间件链是安全
	//     策略，归代码；
	//   - grpcgw 构建：网关转码面（gatewayRegister 由 register_grpcgw.go 的
	//     init 注入）——契约 server.http.driver=grpc-gateway 选择网关面，REST
	//     请求经转码进入 gRPC service，自动复用同一份 proto 注解校验与手写
	//     校验器：一份 proto 定义，gRPC 与 REST 两条协议共享校验规则。
	if gatewayRegister != nil {
		opts = append(opts, appkit.WithGatewayRegister(gatewayRegister))
	} else {
		opts = append(opts, appkit.WithHTTP(newRouter()))
	}
	// nacos 后端（注册中心 + 配置中心）：默认空操作，-tags nacos 时接入。
	opts = append(opts, nacosBootstrapOptions()...)
	return opts
}

// newRouter 组织业务 HTTP 路由（gin 引擎 + 中间件链 + 示例路由组）。
func newRouter() *gin.Engine {
	router := gin.New()
	router.Use(mid.Recovery(), mid.RequestIDMiddleware(), mid.Logging())
	exampleRoutes(router)
	return router
}

// osExit 抽离以便后续测试替换；默认调用 os.Exit。
var osExit = func(code int) { os.Exit(code) }

// newGRPCServerOptions 构造 gRPC 服务器选项（拦截器链）。
//
// 拦截器顺序（由外到内）：请求 ID → 可观测性 → 默认值填充 → 校验。
// 认证/授权（Authn/Authz）接入用户系统后，通过注入 TokenExtractor /
// UserRetriever / Authorizer 串到此链即可（详见 pkg/middleware/grpc/authn.go、
// authz.go 预留接口）。
//
// 抽成函数供 main 与 e2e 测试**共用**：
// 曾因测试里另写一份而漏掉 ValidatorInterceptor，导致「非法请求居然通过了」
// 却让测试看起来在跑 —— 复用同一构造函数可杜绝这类「测试与生产不一致」。
//
// 校验器：拦截器接受回调（依赖倒置），具体实现由注入方决定，
// 本包不绑定任何校验库。三种接法：
//
//	// (a) protovalidate：读取 proto 的 buf.validate 注解（声明式字段规则）
//	//     需先 go get buf.build/go/protovalidate（会引入 cel-go，按需引入）
//	grpcmw.ValidatorInterceptor(func(ctx context.Context, rq any) error {
//	    msg, ok := rq.(proto.Message)
//	    if !ok {
//	        return nil
//	    }
//	    return protovalidate.Validate(msg)
//	})
//
//	// (b) pkg/validation 分发器：复杂命令式逻辑（查库/权限），框架自带零外部依赖
//	v, err := validation.NewValidator(myValidator{})
//	grpcmw.ValidatorInterceptor(v.Validate)
//
//	// (c) 串联：先跑注解规则，再跑复杂逻辑（grpcgw 构建即此接法）
func newGRPCServerOptions() []grpc.ServerOption {
	unaryInterceptors := []grpc.UnaryServerInterceptor{
		// ErrorInterceptor 必须在最外层：收口转换内层（校验等）抛出的错误，
		// 否则 *berrors.Error 的 Code/Reason/Details 会在 gRPC 边界丢失，
		// 客户端只能看到一个空的 Unknown（详见该函数注释）。
		grpcmw.ErrorInterceptor(),
		grpcmw.RequestIDInterceptor(),
		grpcmw.UnaryObservability(),
		grpcmw.DefaulterInterceptor(),
		// 校验回调随域自持（greetv1.Validator：第 1 层 proto 注解 + 第 2 层
		// 手写规则，见 greetv1/greet.go）。默认构建同样生效——域迁移后
		// GreetService 在两种构建下都真实注册，链路无差别。
		grpcmw.ValidatorInterceptor(greetv1.Validator),
	}
	return []grpc.ServerOption{grpc.ChainUnaryInterceptor(unaryInterceptors...)}
}

// gatewayRegister 是 grpc-gateway 转码注册回调（REST → gRPC 转码）。
//
// 默认 nil：HTTP 面走 gin 演示路由。启用 `go run -tags grpcgw` 后，
// 由 register_grpcgw.go 的 init 注入 registerGateway，配合契约
// server.http.driver=grpc-gateway（configs/bald-demo.yaml）接入网关转码面。
//
// 用回调而非直接构造，是因为 GatewayServer 依赖 grpc-gateway（较重），
// 默认构建不该引入它。
// 网关也不是独立服务器：同一 server.http 段只跑一个面，模式由契约
// server.http.driver 驱动（与 bootstrap.ServerRegistry 的装配语义一致）。
var gatewayRegister func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error)

// 注：e2e 测试与 main 同包（package main，见 greet_e2e_test.go），
// 可直接访问 newAppOptions / newGRPCServerOptions，无需导出包装。
// 这是把测试放在 bald/ 目录内而非独立 tests/ 目录的原因。

// exampleRoutes 演示 bald 的 HTTP 路由约定：路由注册直接由业务完成（使用 gin 引擎），
// handler 内部用强绑定 gin 的泛型流水线（web.HandleAllRequest / HandleJSONRequest /
// HandleUriRequest）完成绑定→校验→响应，错误统一由 web 按 pkg/berrors 映射 HTTP 状态
// 码（被 %w 包裹的错误也会正确拆链映射，不会误落 500）。
//
// web 强绑定 gin，路径参数由 gin 原生 ShouldBindUri 处理（结构体用 uri tag），
// 无需额外的上下文桥接。
func exampleRoutes(e *gin.Engine) {
	// 版本组：在根中间件（Recovery/RequestID/Logging）之后叠加 CORS 子链。
	v1 := e.Group("/v1", mid.CORS(mid.DefaultCORS()))

	// 健康检查：直接回字符串，不经过结构化响应。
	v1.GET("/ping", func(c *gin.Context) {
		_, _ = c.Writer.Write([]byte("pong\n"))
	})

	// 注：POST /v1/greet 已随 greet 域迁入 greetv1 包（Domain.Routes 自持
	// 注册，行为逐行等价）——main 不再出现域的路由知识。

	// URI 通配符示例：/v1/users/:id 经 gin 原生 ShouldBindUri 绑定到 req.ID
	// （需 uri:"id" tag，与路径变量名一致）。
	type userReq struct {
		ID string `json:"id" uri:"id"`
	}
	v1.GET("/users/:id", func(c *gin.Context) {
		web.HandleUriRequest[userReq, map[string]string](c,
			func(_ context.Context, u *userReq) (map[string]string, error) {
				return map[string]string{"id": u.ID}, nil
			})
	})

	// 结构化示例②：多源绑定（URI > Query > JSON 后者覆盖）+ 校验器。
	// GET /v1/articles/:id?lang=zh   body: {"title":"hello"}
	type articleReq struct {
		ID    string `json:"id" uri:"id"` // 来自 URI 路径变量
		Lang  string `json:"lang"`        // 来自 Query（?lang=zh）
		Title string `json:"title"`       // 来自 JSON body
	}
	type articleResp struct {
		ID    string `json:"id"`
		Lang  string `json:"lang"`
		Title string `json:"title"`
	}
	validateArticle := func(_ context.Context, a *articleReq) error {
		if a.Title == "" {
			return berrors.BadRequest("EMPTY_TITLE").WithMessage("title 不能为空")
		}
		// 注意：绑定阶段已结束，默认值应在 Defaulter.Default() 中填充
		// （见下方 articleReq 的 Default 方法），这里只做校验。
		if a.Lang == "" {
			a.Lang = "en"
		}
		return nil
	}
	v1.POST("/articles/:id", func(c *gin.Context) {
		web.HandleAllRequest[articleReq, articleResp](c,
			func(_ context.Context, a *articleReq) (articleResp, error) {
				return articleResp{ID: a.ID, Lang: a.Lang, Title: a.Title}, nil
			},
			validateArticle)
	})
}

// 附：configs/bald-demo.yaml 示例（键路径与契约字段一致）
//
//	server:
//	  http:
//	    addr: ":8080"
//	    # driver 只在声明了网关能力（-tags grpcgw）时被消费：
//	    # grpc-gateway = server.http 端口跑网关转码面；其余值/留空走业务 handler。
//	    driver: "grpc-gateway"
//	  grpc:
//	    addr: ":9090"
//	logger:
//	  type: "slog"
//	  slog:
//	    level: "info"
//
// 远程配置中心（etcd/nacos）中可存同样结构（yaml/json 均可）作为基准；
// 本地文件中的同名 key 会覆盖它。例如远程 server.http.addr=:8080、
// 本地 server.http.addr=:18080，最终生效 :18080。
// nacos 接入见 register_nacos.go（build tag nacos，需先 go get 对应 contrib）。
