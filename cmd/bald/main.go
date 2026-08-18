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
//	# 远程配置中心（etcd）：先 go get github.com/go-kratos/kratos/v3/contrib/config/etcd/v3
//	go run ./cmd/bald --config=configs/bald-demo.yaml   # 远程作基准，本地覆盖
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/spf13/viper"
	"google.golang.org/grpc"

	"github.com/kalandramo/bald/pkg/appkit"
	baldoptions "github.com/kalandramo/bald/pkg/options"
	"github.com/kalandramo/bald/pkg/server"
)

func main() {
	httpOpts := baldoptions.NewHTTPOptions()
	grpcOpts := baldoptions.NewGRPCOptions()

	// 1. 构造协议服务器（均实现 server.Server 契约，含 Endpoint）。
	httpSrv := server.NewHTTPServer(httpOpts, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello from bald http\n"))
	}))

	grpcSrv := server.NewGRPCServerWithRegister(grpcOpts, nil, func(s *grpc.Server) {
		// 在此注册你的 gRPC service 实现，例如：
		// pb.RegisterYourServer(s, yourImpl)
	})

	// 2. 用 appkit 组合层运行（自研编排：并发启停 + 优雅停机 + 防重入 + 可观察）。
	var app *appkit.AppKit
	app = appkit.New(
		appkit.Name("bald-demo"),
		appkit.Version("v0.1.0"),
		appkit.StopTimeout(15*time.Second),

		// --- 启动期配置（onexstack 风格 --config + env + flag + 可选远程配置中心）---
		//
		// 优先级（高 → 低）：flag > 本地文件 > 环境变量 > 远程配置。
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
			log.Printf("config changed: http.addr=%s grpc.addr=%s",
				v.GetString("http.addr"), v.GetString("grpc.addr"))
			// 热重载业务 options（示例：仅日志，真实场景可在此重绑端口等）。
			_ = v.Unmarshal(httpOpts)
			_ = v.Unmarshal(grpcOpts)
		}),

		// appkit.Registrar(registrar), // 可选：注入 etcd/consul/nacos 注册中心
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
			log.Printf("loaded config: http.addr=%s grpc.addr=%s",
				httpOpts.Addr, grpcOpts.Addr)
			return nil
		}),
		appkit.AfterStart(func(ctx context.Context) error {
			log.Printf("bald-demo started: grpc=%s http=%s", grpcSrv.Endpoint(), httpSrv.Endpoint())
			return nil
		}),

		// 4. 停机钩子（可选）。
		appkit.BeforeStop(func(ctx context.Context) error {
			log.Printf("bald-demo stopping...")
			return nil
		}),
	)

	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("bald app exited: %v", err)
	}
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
