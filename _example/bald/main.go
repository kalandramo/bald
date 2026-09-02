// Command bald 是 bald 服务框架的示例入口：
// 使用 appkit 组合层管理一个 HTTP 服务与一个 gRPC 服务，
// 并演示本地配置、环境变量、命令行 flag 与远程配置中心（etcd/nacos）的接入方式。
//
// 运行：
//
//	# 本地文件 + 环境变量 + flag（无需远程配置中心即可运行）
//	go run ./_example/bald --config=configs/bald-demo.yaml
//	BALD_DEMO_HTTP_ADDR=:18080 go run ./_example/bald        # 环境变量覆盖 http.addr
//	go run ./_example/bald --http.addr=:18080                # flag 优先级最高
//
//	# 多环境（本地按 bald-demo-prod.yaml 选择默认文件）
//	go run ./_example/bald --env=prod
//
//	# 切换日志格式 / 级别（--log.* 由 pkg/log 提供）
//	go run ./_example/bald --log.format=json --log.level=debug
//
//	# 验证 HTTP 示例路由（server.NewHTTPServer 挂载 gin.Engine）：
//	curl -i http://127.0.0.1:8080/v1/ping
//	curl -i -XPOST http://127.0.0.1:8080/v1/greet -d '{"name":"bald"}'
//	curl -i -XPOST 'http://127.0.0.1:8080/v1/articles/42?lang=zh' -d '{"title":"hi"}'
//
//	# grpc-gateway transcoding 示例（需本地 protoc 工具链生成代码后启用）：
//	#   见 docs/devel/zh-CN/grpc-gateway 配置与 transcoding.md
//	#   生成：cd _example/bald && make proto && cd ../..
//	#   运行：go run -tags grpcgw ./_example/bald --config=_example/bald/configs/bald-demo.yaml
//	#   验证：curl -i -XPOST http://127.0.0.1:<gateway>/v1/greet -d '{"name":"bald"}'
//
//	# Windows (PowerShell)：用 --% 关闭 PowerShell 解析，JSON 内双引号以 \ 转义
//	curl.exe -i http://127.0.0.1:8080/v1/ping
//	curl.exe --% -i -XPOST http://127.0.0.1:8080/v1/greet -H "Content-Type: application/json" -d "{\"name\":\"bald\"}"
//	curl.exe --% -XPOST "http://127.0.0.1:8080/v1/articles/42?lang=zh" -H "Content-Type: application/json" -d "{\"title\":\"hi\"}"
//
//	# 远程配置中心（etcd）：先 go get github.com/go-kratos/kratos/v3/contrib/config/etcd/v3
//	go run ./_example/bald --config=configs/bald-demo.yaml   # 远程作基准，本地覆盖
package main

import (
	"context"
	"fmt"
	"log/slog" // 仅用于 ContextWithAttrs 的 slog.Attr 构造（如 log.String）。
	"os"
	"os/exec"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"google.golang.org/grpc"

	usercmd "github.com/kalandramo/bald/example/bald/user"

	"github.com/kalandramo/bald/pkg/appkit"
	baldconf "github.com/kalandramo/bald/pkg/conf"
	confv1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/config/v1"
	baldconfig "github.com/kalandramo/bald/pkg/config"
	berrors "github.com/kalandramo/bald/pkg/berrors"
	baldlog "github.com/kalandramo/bald/pkg/log"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	grpcmw "github.com/kalandramo/bald/pkg/middleware/grpc"
	"github.com/kalandramo/bald/pkg/registry/inmemory"
	"github.com/kalandramo/bald/pkg/server"
	"github.com/kalandramo/bald/pkg/web"
)

func serveRunE(_ *cobra.Command, _ []string) error {
	// 0. 框架级配置：proto 是唯一真相源，直接持有 Bootstrap 指针。
	//    server 层（http/grpc）直接消费其中的 confv1.Http / confv1.Grpc 子消息，
	//    不再经由 pkg/options 中间层（P1 阶段 2 已废弃 options）。
	bootstrap := baldconf.NewBootstrap()
	bootstrap.Http.Addr = ":8080" // 明文 HTTP；启用 HTTPS 时设 Tls.Enabled=true 并提供 tls 字段

	// 0. 日志系统接入（两阶段）。
	//    阶段 A：先用默认配置装一个 Logger，保证启动期有日志可用。
	//    阶段 B：在 appkit.BeforeStart 里按最终配置重建（见下方），
	//           因为 --log.* 的真实取值要等配置加载完才知道。
	logOpts := baldlog.NewOptions()
	setLogger(logOpts)

	// 1. 构造协议服务器（均实现 server.Server 契约，含 Endpoint）。
	//    共享同一个 readiness 探针，使 HTTP /readyz 与 gRPC health 状态对称联动。
	ready := func(ctx context.Context) error {
		// TODO: 在此检查业务依赖（如 DB ping、下游连通性）。返回 nil=就绪，error=未就绪。
		// 未就绪时：HTTP /readyz 返回 503，gRPC health 置 NOT_SERVING（K8s 摘流量）。
		return nil
	}
	// 业务 HTTP 路由：直接用 gin 引擎组织（gin.Engine 实现 http.Handler），
	// 路由注册由业务自己完成，服务器层 NewHTTPServer 签名不变（*gin.Engine 即 http.Handler）。
	router := gin.New()
	router.Use(mid.Recovery(), mid.RequestID(), mid.Logging())
	exampleRoutes(router)
	httpSrv := server.NewHTTPServer(bootstrap.GetHttp(), router, ready)

	// gRPC 拦截器链（由外到内）：请求 ID → 可观测性 → 默认值填充 → 校验。
	// 认证/授权（Authn/Authz）在 babel 接入用户系统后，通过注入 TokenExtractor /
	// UserRetriever / Authorizer 串到此链即可（详见 pkg/middleware/grpc/authn.go、
	// authz.go 预留接口）。
	//
	// 校验器：拦截器接受一个回调（依赖倒置），具体实现由注入方决定，
	// 本包不绑定任何校验库（与 P5「核心零后端依赖」治理一致）。三种接法：
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
	//	if err != nil {
	//	    // 方法名拼写错误等会在此暴露（P2 修复），不会静默失效
	//	}
	//	grpcmw.ValidatorInterceptor(v.Validate)
	//
	//	// (c) 串联：先跑注解规则，再跑复杂逻辑
	//
	// 此处演示 (b)：greetValidator 是包级回调变量。
	//   - 默认编译（无 grpcgw tag）：它为 nil，拦截器退化为显式的空操作；
	//   - `go run -tags grpcgw`：由 register_grpcgw.go 的 init 注入真实校验器
	//     （针对 baldv1.GreetRequest，见该文件）。
	// 注意 NewValidator 现在返回 error —— 方法名与请求类型名不符时会明确报错，
	// 不会像旧实现那样静默跳过导致「以为有校验其实没有」。
	grpcSrv := server.NewGRPCServerWithRegister(
		bootstrap.GetGrpc(),
		newGRPCServerOptions(),
		registerGRPCService,
		ready,
	)

	// 2. 组装 AppKit（自研编排：并发启停 + 优雅停机 + 防重入 + 可观察）。
	//    配置解析（BeforeStart）、校验链路等都在 newApp 内，
	//    与 e2e 测试复用同一份构造逻辑。
	app := newApp(bootstrap, logOpts, httpSrv, grpcSrv, ready)

	// 2.1 可选的 nacos 后端（注册中心 + 配置中心）。
	//     默认构建（无 nacos tag）下 applyNacosBackends 是空操作；
	//     用 -tags nacos 构建时它才会接入真实的 nacos（见 register_nacos.go）。
	applyNacosBackends(app)

	// 3. 运行：阻塞直到收到信号或任一服务器退出。
	if err := app.Run(context.Background()); err != nil {
		baldlog.GetLogger().Error(context.Background(), "bald app exited", "error", err)
		return err
	}
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

// newApp 组装 AppKit（HTTP + gRPC + 配置 + 校验链路）。
//
// 抽成函数是为了让 e2e 测试能复用**同一份**真实构造逻辑
// （见 tests/e2e/greet_e2e_test.go），而不是在测试里另抄一份 ——
// 复制出来的应用与真实运行的不一致，测试就失去回归价值。
//
// 注意 main() 里的 osExit / setLogger 等进程级副作用保持在 main 中，
// 不在 newApp 内，以便测试安全调用。
func newApp(
	bootstrap *confv1.Bootstrap,
	logOpts *baldlog.Options,
	httpSrv *server.HTTPServer,
	grpcSrv *server.GRPCServer,
	ready server.ReadinessFunc,
) *appkit.AppKit {
	// 先声明再赋值：BeforeStart 闭包需要在构造参数中引用 app 自身（取 app.Viper()）。
	var app *appkit.AppKit
	app = appkit.New(
		appkit.Name("bald-demo"),
		appkit.Version("v0.1.0"),
		appkit.StopTimeout(15*time.Second),

		// --- 启动期配置（面向 K8s/容器部署：--config + env + flag + 可选远程配置中心）---
		//
		// 优先级（高 → 低，viper 默认语义）：flag > 环境变量 > 本地文件 > 远程配置。
		// 本地文件缺失不报错，因此可只用远程/flag 配置。

		// 2.1 本地配置文件：等价 --config=configs/bald-demo.yaml；
		//     也可不传，改为按 Name/Env 自动查找（见 2.2）。
		appkit.ConfigFile("configs/bald-demo.yaml"),

		// 2.2 多环境（路线 1）：非空时本地按 {Name}-{Env}.yaml 选默认文件
		//     （如 bald-demo-prod.yaml）；远程 path 由后端构造时拼接。
		// appkit.Env("prod"),

		// 2.3 本地文件热更新（fsnotify）：文件变更触发 OnConfigChange。
		appkit.WatchConfigFile(true),

		// 2.4 远程配置中心（可选）：通过 config.RemoteSource 接入。
		//     推荐用 config.FromKratosSource 桥接 kratos contrib 后端，
		//     远程作"基准"，本地文件覆盖远程（详见 docs/config-center-design.md）。
		//
		//     etcd 后端示例（先：go get github.com/go-kratos/kratos/v3/contrib/config/etcd/v3）：
		//     import (
		//         "github.com/kalandramo/bald/pkg/config"
		//         etcdclient "go.etcd.io/etcd/client/v3"
		//         etcdconfig "github.com/go-kratos/kratos/v3/contrib/config/etcd/v3"
		//     )
		//     cli, _ := etcdclient.New(etcdclient.Config{Endpoints: []string{"127.0.0.1:2379"}})
		//     appkit.RemoteConfig(config.FromKratosSource(
		//         etcdconfig.New(cli, etcdconfig.WithPath("/config/bald-demo/prod.yaml"))))
		//
		//     nacos 后端：见 register_nacos.go（build tag `nacos`，
		//     需先 go get github.com/go-kratos/kratos/v3/contrib/config/nacos/v3
		//     + github.com/go-kratos/kratos/v3/contrib/registry/nacos/v3），
		//     用 -tags nacos 构建即自动接入注册中心 + 配置中心。

		// 2.5 业务 flag 接入（关键）：把配置对象的 flag 注册进 viper override 层。
		//     prefix 即配置键前缀，四源路径由此统一：
		//       --http.addr ⇔ http.addr ⇔ BALD_DEMO_HTTP_ADDR ⇔ 配置文件 http.addr
		//     log.Options 自带 --log.* 前缀，因此 prefix 传空串。
		//     http/grpc 直接绑定 Bootstrap 的 proto 子消息，flag 改的是同一个对象，
		//     server 层在 Start 时直接读它（无需回填）。
		appkit.Bind("http", bootstrap.GetHttp()),
		appkit.Bind("grpc", bootstrap.GetGrpc()),
		appkit.Bind("", logOpts),

		// 2.6 配置热更新回调：本地文件或远程变更均触发。
		//     proto 契约（Bootstrap）是唯一持有对象，server 层直接消费其指针，
		//     热更新时重新 Unmarshal 即生效。
		appkit.OnConfigChange(func(v *viper.Viper) {
			logger := baldlog.GetLogger()
			logger.Info(context.Background(), "config changed",
				"http.addr", v.GetString("http.addr"), "grpc.addr", v.GetString("grpc.addr"))
			if err := baldconfig.Unmarshal(v, bootstrap); err != nil {
				logger.Error(context.Background(), "reload config failed", "error", err)
				return
			}
		}),

		// 2.7 服务注册中心（可选）。
		//     默认用 inmemory 实现端到端演示（零外部依赖、可真跑），
		//     覆盖 register -> 运行 -> deregister 全流程，且能验证 :0 动态端口聚合注册。
		//     生产用 etcd/consul/nacos：经 appkit.Registrar(registry.FromKratos(kratosReg))
		//     桥接 kratos contrib（nacos 接线见 register_nacos.go 的 `nacos` build tag，
		//     用 -tags nacos 构建时由 applyNacosBackends 一并接入注册中心 + 配置中心）。
		appkit.Registrar(inmemory.New()),
		// 服务器集合：gRPC + HTTP（+ grpc-gateway，见 buildServers）。
		appkit.Servers(buildServers(bootstrap, httpSrv, grpcSrv, ready)...),

		// 3. 启动前按 proto 契约解析配置。
		//    flag / 远程 / 本地文件 / 环境变量 的合并结果都在 app.Viper() 中；
		//    用 conf.Bootstrap（Protobuf）接收，字段名与类型是编译期可查的，
		//    而非依赖 mapstructure tag 的字符串匹配（写错键名会静默落到零值）。
		appkit.BeforeStart(func(ctx context.Context) error {
			v := app.Viper()
			if v == nil {
				return nil
			}

			// bootstrap 已被 flag 注册指向同一对象，此处 Unmarshal 把 viper 合并结果写回；
			// 由于是同一个指针，server 层 Start 时直接读到最终值。
			if err := baldconfig.Unmarshal(v, bootstrap); err != nil {
				return fmt.Errorf("unmarshal config: %w", err)
			}
			if err := baldconf.Validate(bootstrap); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}

			// 日志阶段 B：按最终配置重建全局 Logger。
			setLogger(baldconf.LogOptions(bootstrap.GetLogger()))
			logger := baldlog.GetLogger()

			logger.Info(ctx, "loaded config",
				"http.addr", bootstrap.GetHttp().GetAddr(),
				"http.tls.enabled", bootstrap.GetHttp().GetTls().GetEnabled(),
				"grpc.addr", bootstrap.GetGrpc().GetAddr(),
				"log.level", bootstrap.GetLogger().GetLevel())
			return nil
		}),
		appkit.AfterStart(func(ctx context.Context) error {
			// 通过 ContextWithAttrs 把请求范围属性挂到 ctx，该 ctx 内的日志自动携带。
			ctx = baldlog.ContextWithAttrs(ctx,
				slog.String("stage", "started"), slog.String("grpc", grpcSrv.Endpoint()))
			baldlog.GetLogger().Info(ctx, "bald-demo started", "http", httpSrv.Endpoint())
			return nil
		}),

		// 4. 停机钩子（可选）。
		appkit.BeforeStop(func(ctx context.Context) error {
			baldlog.GetLogger().Info(ctx, "bald-demo stopping")
			return nil
		}),
	)
	return app
}

// osExit 抽离以便后续测试替换；默认调用 os.Exit。
var osExit = func(code int) { os.Exit(code) }

// greetValidator 是注入给 gRPC 校验拦截器的回调（依赖倒置：拦截器不绑定具体校验库）。
//
// 默认编译（无 grpcgw build tag）下没有注册任何 gRPC service，因此它是 nil，
// 拦截器退化为显式的空操作——注意这与旧实现不同：旧实现传
// `validation.NewValidator(nil)` 会得到一个「看似注册了、实际什么都不校验」的
// 校验器，问题被隐藏；现在 nil 就是 nil，语义可见。
//
// 启用 `go run -tags grpcgw` 后，由 register_grpcgw.go 的 init 注入真实校验器
// （针对 baldv1.GreetRequest 的 ValidateGreetRequest）。
var greetValidator grpcmw.MessageValidator

// registerGRPCService 是 gRPC service 注册回调，供 server.NewGRPCServerWithRegister 使用。
//
// 默认编译（无 grpcgw tag）下它是空实现——业务在此注册自己的实现即可
// （pb.RegisterYourServer(s, yourImpl)）。
//
// 启用 `go run -tags grpcgw` 后，由 register_grpcgw.go 的 init 替换为真实注册
// （baldv1.RegisterGreetServiceServer），无需再手工改本文件。
var registerGRPCService = func(s *grpc.Server) {
	// 在此注册你的 gRPC service 实现。
}

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
// 本包不绑定任何校验库（与 P5「核心零后端依赖」治理一致）。
// grpcgw 构建下 greetValidator 由 register_grpcgw.go 的 init 注入，
// 串联 protovalidate（proto 注解）+ pkg/validation（复杂逻辑）两层。
func newGRPCServerOptions() []grpc.ServerOption {
	unaryInterceptors := []grpc.UnaryServerInterceptor{
		// ErrorInterceptor 必须在最外层：收口转换内层（校验等）抛出的错误，
		// 否则 *berrors.Error 的 Code/Reason/Details 会在 gRPC 边界丢失，
		// 客户端只能看到一个空的 Unknown（详见该函数注释）。
		grpcmw.ErrorInterceptor(),
		grpcmw.RequestIDInterceptor(),
		grpcmw.UnaryObservability(),
		grpcmw.DefaulterInterceptor(),
		grpcmw.ValidatorInterceptor(greetValidator),
	}
	return []grpc.ServerOption{grpc.ChainUnaryInterceptor(unaryInterceptors...)}
}

// buildServers 组装要运行的服务器集合：gRPC + HTTP，可选 grpc-gateway。
//
// grpc-gateway 只在注入了 gatewayFactory（即 -tags grpcgw 构建）时挂载。
// 挂上后 REST 请求会转码到 gRPC service，**自动复用同一份 proto 注解校验**
// （buf.validate）与手写校验器 —— 这正是 grpc-gateway 的价值：
// 一份 proto 定义，gRPC 与 REST 两条协议共享校验规则。
//
// 抽成函数是为了让 main 与 e2e 测试共用（见 greet_e2e_test.go），
// 避免测试里另写一份导致「测得的东西和跑的不一样」。
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

	// gateway 需要连到 gRPC 服务，故用其监听地址。
	// 注意：地址必须是**可连接**的（如 :9090），不能是 :0
	// （:0 是监听语义，转发时无从得知实际端口）—— e2e 测试会显式分配空闲端口。
	gwCfg := &confv1.Http{Addr: gatewayAddr}
	gw, err := gatewayFactory(gwCfg, bootstrap.GetGrpc(), ready)
	if err != nil {
		// 网关构造失败不应静默降级（否则 REST 路由凭空消失、无人知晓），
		// 直接 panic 让问题在启动期暴露；也符合 appkit 的 fail-fast 风格。
		panic("build gateway server: " + err.Error())
	}
	return append(servers, gw)
}

// gatewayFactory 构造 grpc-gateway 服务器（REST → gRPC 转码）。
//
// 默认 nil：不挂网关。启用 `go run -tags grpcgw` 后，
// 由 register_grpcgw.go 的 init 注入 newGatewayWithGreet。
//
// 用工厂函数而非直接构造，是因为 GatewayServer 依赖 grpc-gateway（较重），
// 默认构建不该引入它（与 P5「核心零重依赖」、P6「依赖倒置」一致）。
var gatewayFactory func(httpCfg *confv1.Http, grpcBackend *confv1.Grpc, ready server.ReadinessFunc) (*server.GatewayServer, error)

// gatewayAddr 是 grpc-gateway 的监听地址，与 HTTP 主服务（http.addr）分开，
// 避免端口冲突。e2e 测试会覆盖它（用空闲端口）。
var gatewayAddr = ":8081"

// 注：e2e 测试与 main 同包（package main，见 greet_e2e_test.go），
// 可直接访问 newApp / registerGRPCService / newGRPCServerOptions，无需导出包装。
// 这是把测试放在 bald/ 目录内而非独立 tests/ 目录的原因。

// setLogger 按给定配置重建全局 Logger，并保留脱敏装饰器。
// 启动期先按默认配置装一次（阶段 A），配置加载完成后再按最终配置重建（阶段 B）。
func setLogger(opts *baldlog.Options) {
	baldlog.SetLogger(baldlog.NewSlogLogger(opts,
		baldlog.WithFilter(baldlog.FilterKey("password")),
		baldlog.WithFilter(baldlog.FilterKey("token")),
		baldlog.WithAttrs(slog.String("service.name", "bald-demo")),
	))
}

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

	// 结构化示例①：纯 JSON 绑定 + 业务错误（返回 400 + ErrorResponse）。
	type greetReq struct {
		Name string `json:"name"`
	}
	type greetResp struct {
		Greet string `json:"greet"`
	}
	v1.POST("/greet", func(c *gin.Context) {
		web.HandleJSONRequest[greetReq, greetResp](c,
			func(_ context.Context, g *greetReq) (greetResp, error) {
				if g.Name == "" {
					// BadRequest 的代码-原因-消息三者分离：Reason 稳定可枚举（客户端可匹配），
					// Message 给人看，二者经统一 ErrorResponse 返回。
					return greetResp{}, berrors.BadRequest("EMPTY_NAME").
						WithMessage("name 不能为空")
				}
				return greetResp{Greet: "hello, " + g.Name}, nil
			})
	})

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

// 附：configs/bald-demo.yaml 示例
//
//	http:
//	  addr: ":8080"
//	grpc:
//	  addr: ":9090"
//
// 远程 etcd 中 /config/bald-demo/prod.yaml 可存同样结构（yaml/json 均可），
// 作为基准；本地文件中的同名 key 会覆盖它。例如远程 http.addr=:8080、
// 本地 http.addr=:18080，最终生效 :18080。
