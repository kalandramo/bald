// Command bald 是 bald 服务框架的示例入口：
// 使用 appkit 组合层管理一个 HTTP 服务与一个 gRPC 服务，
// 并演示本地配置、环境变量、命令行 flag 与远程配置中心（etcd/nacos）的接入方式。
//
// 运行：
//
//	# 本地文件 + 环境变量 + flag（无需远程配置中心即可运行）
//	go run ./cmd/bald --config=configs/bald-demo.yaml
//	BALD_DEMO_HTTP_ADDR=:18080 go run ./cmd/bald        # 环境变量覆盖 http.addr
//	go run ./cmd/bald --http.addr=:18080                # flag 优先级最高
//
//	# 多环境（本地按 bald-demo-prod.yaml 选择默认文件）
//	go run ./cmd/bald --env=prod
//
//	# 切换日志格式 / 级别（--log.* 由 pkg/log 提供）
//	go run ./cmd/bald --log.format=json --log.level=debug
//
//	# 验证 HTTP 示例路由（server.NewHTTPServer 挂载 web.Router）：
//	curl -i http://127.0.0.1:8080/v1/ping
//	curl -i -XPOST http://127.0.0.1:8080/v1/greet -d '{"name":"bald"}'
//	curl -i -XPOST 'http://127.0.0.1:8080/v1/articles/42?lang=zh' -d '{"title":"hi"}'
//
//	# 远程配置中心（etcd）：先 go get github.com/go-kratos/kratos/v3/contrib/config/etcd/v3
//	go run ./cmd/bald --config=configs/bald-demo.yaml   # 远程作基准，本地覆盖
package main

import (
	"context"
	"log/slog" // 仅用于 ContextWithAttrs 的 slog.Attr 构造（如 log.String）。
	"net/http"
	"os"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"google.golang.org/grpc"

	berrors "github.com/kalandramo/bald/pkg/errors"
	baldlog "github.com/kalandramo/bald/pkg/log"
	"github.com/kalandramo/bald/pkg/appkit"
	baldoptions "github.com/kalandramo/bald/pkg/options"
	"github.com/kalandramo/bald/pkg/registry/inmemory"
	"github.com/kalandramo/bald/pkg/server"
	"github.com/kalandramo/bald/pkg/web"
)

func main() {
	httpOpts := baldoptions.NewSecureServingOptions()
	httpOpts.Addr = ":8080" // 明文 HTTP（Enabled 默认 false）；启用 HTTPS 时设 Enabled=true 并提供 tls 字段
	grpcOpts := baldoptions.NewGRPCOptions()

	// 0. 日志系统接入（方案 A：进程入口 bootstrap 初始化全局 Logger）。
	//    通过 --log.level / --log.format / --log.output-paths 多源配置；
	//    注册 FilterKey 脱敏装饰器，敏感字段（如 password/token）自动替换为 ***。
	logOpts := baldlog.NewOptions()
	logOpts.AddFlags(pflag.CommandLine)
	baldlog.SetLogger(baldlog.NewSlogLogger(logOpts,
		baldlog.WithFilter(baldlog.FilterKey("password")),
		baldlog.WithFilter(baldlog.FilterKey("token")),
		baldlog.WithAttrs(slog.String("service.name", "bald-demo")),
	))
	// 框架内部与业务统一经由 log.GetLogger() 取同一份实例。
	logger := baldlog.GetLogger()

	// 业务 flag 注册（带前缀，支持多组件复用同一 options 类型）。
	// 优先级：命令行 flag > 环境变量 > 本地文件 > 远程配置。
	// SecureServingOptions 内嵌 TLSOptions，TLS 子字段自动展开为 --bald-demo.http.tls.*。
	appPrefix := "bald-demo"
	httpOpts.AddFlags(pflag.CommandLine, baldoptions.Join(appPrefix, "http"))
	grpcOpts.AddFlags(pflag.CommandLine, baldoptions.Join(appPrefix, "grpc"))

	// 1. 构造协议服务器（均实现 server.Server 契约，含 Endpoint）。
	//    共享同一个 readiness 探针，使 HTTP /readyz 与 gRPC health 状态对称联动。
	ready := func(ctx context.Context) error {
		// TODO: 在此检查业务依赖（如 DB ping、下游连通性）。返回 nil=就绪，error=未就绪。
		// 未就绪时：HTTP /readyz 返回 503，gRPC health 置 NOT_SERVING（K8s 摘流量）。
		return nil
	}
	// 业务 HTTP 路由：用 web.Router（实现 http.Handler）组织，业务模块通过
	// ApplyTo 把自己的路由挂载到版本组，服务器层 NewHTTPServer 签名不变。
	router := web.NewRouter(web.Recovery(), web.RequestID(), web.Logging())
	_ = exampleHandler{}.ApplyTo(router)
	httpSrv := server.NewHTTPServer(httpOpts, router, ready)

	grpcSrv := server.NewGRPCServerWithRegister(grpcOpts, nil, func(s *grpc.Server) {
		// 在此注册你的 gRPC service 实现，例如：
		// pb.RegisterYourServer(s, yourImpl)
	}, ready)

	// 2. 用 appkit 组合层运行（自研编排：并发启停 + 优雅停机 + 防重入 + 可观察）。
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
		//     (a) etcd 后端（先：go get github.com/go-kratos/kratos/v3/contrib/config/etcd/v3）
		//     import (
		//         "github.com/kalandramo/bald/pkg/config"
		//         etcdclient "go.etcd.io/etcd/client/v3"
		//         etcdconfig "github.com/go-kratos/kratos/v3/contrib/config/etcd/v3"
		//     )
		//     cli, _ := etcdclient.New(etcdclient.Config{Endpoints: []string{"127.0.0.1:2379"}})
		//     src := config.FromKratosSource(
		//         etcdconfig.New(cli, etcdconfig.WithPath("/config/bald-demo/prod.yaml")))
		//     appkit.RemoteConfig(src),
		//
		//     (b) nacos 后端（先：go get github.com/go-kratos/kratos/contrib/config/nacos/v3）
		//     import (
		//         "github.com/kalandramo/bald/pkg/config"
		//         nacosclients "github.com/nacos-group/nacos-sdk-go/clients"
		//         "github.com/nacos-group/nacos-sdk-go/clients/config_client"
		//         "github.com/nacos-group/nacos-sdk-go/common/constant"
		//         "github.com/nacos-group/nacos-sdk-go/vo"
		//         nacosconfig "github.com/go-kratos/kratos/contrib/config/nacos/v3"
		//     )
		//     client, _ := nacosclients.NewConfigClient(vo.NacosClientParam{
		//         ClientConfig:  &constant.ClientConfig{NamespaceId: "prod", TimeoutMs: 5000, LogDir: ""},
		//         ServerConfigs: []constant.ServerConfig{{IpAddr: "127.0.0.1", Port: 8848, Scheme: "http", ContextPath: "/nacos"}},
		//     })
		//     // dataID 带扩展名（.yaml）以便 contrib 正确识别格式；
		//     // NewConfigClient 返回即 config_client.IConfigClient，无需断言。
		//     src := config.FromKratosSource(nacosconfig.NewConfigSource(
		//         client,
		//         nacosconfig.WithDataID("bald-demo.yaml"), nacosconfig.WithGroup("DEFAULT_GROUP")))
		//     appkit.RemoteConfig(src),

		// 2.5 配置热更新回调：本地文件或远程变更均触发。
		//     裸 viper 粒度，业务自行 Unmarshal。注意回调内应重新读取最新值，
		//     不要持有已 Unmarshal 的结构体副本。
		appkit.OnConfigChange(func(v *viper.Viper) {
			logger.Info(context.Background(), "config changed",
				"http.addr", v.GetString("http.addr"), "grpc.addr", v.GetString("grpc.addr"))
			// 热重载业务 options（示例：仅日志，真实场景可在此重绑端口等）。
			_ = v.Unmarshal(httpOpts)
			_ = v.Unmarshal(grpcOpts)
		}),

		// 2.6 服务注册中心（可选）。
		//     生产用 etcd/consul/nacos：appkit.KratosRegistrar(kratosReg) 桥接 kratos contrib。
		//     此处用 inmemory 实现端到端演示（零外部依赖、可真跑），
		//     覆盖 register -> 运行 -> deregister 全流程，且能验证 :0 动态端口聚合注册。
		appkit.Registrar(inmemory.New()),
		appkit.Servers(grpcSrv, httpSrv),

		// 3. 启动前从合并后的配置反序列化业务 options。
		//     flag / 远程 / 本地文件 / 环境变量 合并的结果都在 app.Viper() 中。
		appkit.BeforeStart(func(ctx context.Context) error {
			v := app.Viper() // 取启动期加载后的 *viper.Viper
			if v == nil {
				return nil
			}
			if err := v.Unmarshal(httpOpts); err != nil {
				return err
			}
			if err := v.Unmarshal(grpcOpts); err != nil {
				return err
			}
			logger.Info(ctx, "loaded config",
				"http.addr", httpOpts.Addr, "grpc.addr", grpcOpts.Addr)
			return nil
		}),
		appkit.AfterStart(func(ctx context.Context) error {
			// 通过 ContextWithAttrs 把请求范围属性挂到 ctx，该 ctx 内的日志自动携带。
			ctx = baldlog.ContextWithAttrs(ctx,
				slog.String("stage", "started"), slog.String("grpc", grpcSrv.Endpoint()))
			logger.Info(ctx, "bald-demo started", "http", httpSrv.Endpoint())
			return nil
		}),

		// 4. 停机钩子（可选）。
		appkit.BeforeStop(func(ctx context.Context) error {
			logger.Info(ctx, "bald-demo stopping")
			return nil
		}),
	)

	if err := app.Run(context.Background()); err != nil {
		logger.Error(context.Background(), "bald app exited", "error", err)
		osExit(1)
	}
}

// osExit 抽离以便后续测试替换；默认调用 os.Exit。
var osExit = func(code int) { os.Exit(code) }

// exampleHandler 演示 bald 的 HTTP 路由约定：业务模块通过 ApplyTo 把路由挂载
// 到版本组（/v1），路由归属模块、认证等中间件由装配层注入。handler 内部用泛型
// 流水线（HandleAllRequest / HandleJSONRequest / HandleUriRequest）完成
// 绑定→校验→响应，错误统一由 web.WriteResponse 按 pkg/errors.StatusCoder 映射
// HTTP 状态码（被 %w 包裹的错误也会正确拆链映射，不会误落 500）。
type exampleHandler struct{}

func (exampleHandler) Name() string { return "example" }

func (exampleHandler) ApplyTo(r *web.Router, middlewares ...web.Middleware) error {
	// 子组可再叠加中间件（如 CORS），与根/路由级中间件组成由外到内链。
	v1 := r.Group("/v1", append([]web.Middleware{web.CORS(web.DefaultCORS())}, middlewares...)...)

	// 健康检查：直接回字符串，不经过结构化响应。
	v1.HandleFunc("GET", "/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("pong\n"))
	})

	// 结构化示例①：纯 JSON 绑定 + 业务错误（返回 400 + ErrorResponse）。
	type greetReq struct {
		Name string `json:"name"`
	}
	type greetResp struct {
		Greet string `json:"greet"`
	}
	v1.HandleFunc("POST", "/greet", func(w http.ResponseWriter, req *http.Request) {
		web.HandleJSONRequest[greetReq, greetResp](w, req,
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

	// URI 通配符示例：/v1/users/{id} 自动绑定到 req.ID（字段名需与路径变量一致）。
	type userReq struct {
		ID string `json:"id"`
	}
	v1.HandleFunc("GET", "/users/{id}", func(w http.ResponseWriter, req *http.Request) {
		web.HandleUriRequest[userReq, map[string]string](w, req,
			func(_ context.Context, u *userReq) (map[string]string, error) {
				return map[string]string{"id": u.ID}, nil
			})
	})

	// 结构化示例②：多源绑定（URI > Query > JSON 后者覆盖）+ 校验器。
	// GET /v1/articles/{id}?lang=zh   body: {"title":"hello"}
	type articleReq struct {
		ID    string `json:"id"`    // 来自 URI 路径变量
		Lang  string `json:"lang"`  // 来自 Query（?lang=zh）
		Title string `json:"title"` // 来自 JSON body
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
		if a.Lang == "" {
			a.Lang = "en" // 默认值：校验阶段修正，绑定阶段已结束
		}
		return nil
	}
	v1.HandleFunc("POST", "/articles/{id}", func(w http.ResponseWriter, req *http.Request) {
		web.HandleAllRequest[articleReq, articleResp](w, req,
			func(_ context.Context, a *articleReq) (articleResp, error) {
				return articleResp{ID: a.ID, Lang: a.Lang, Title: a.Title}, nil
			},
			validateArticle)
	})

	return nil
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
