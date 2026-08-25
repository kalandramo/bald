# bald

一个融合三方设计精华的 Go 服务框架：

- **onexstack/pkg/app**：启动期 Options + 配置理念（`--config`/viper 由调用方注入）。
- **Kratos**：`transport.Server` 契约与 `registry.Registrar` 接口（可插拔复用）。
- **go-lulu (`wind`)**：自研 App 层精髓——errgroup 并发启停、优雅停机防坑、崩溃级联停止、Run 防重入、可观察通道、`Endpoint()` 动态端口注册。

## 架构

```
bald/
├── pkg/
│   ├── server/               # 协议层：统一 Server 契约 + 协议适配器
│   │   ├── server.go         # Server = Start/Stop/Endpoint；Serve() 独立生命周期
│   │   ├── http_server.go    # net/http（支持 HTTP/HTTPS，动态端口+可达 IP 解析）
│   │   ├── grpc_server.go    # google.golang.org/grpc（自带 health + reflection）
│   │   └── gateway_server.go # grpc-gateway 反向代理
│   ├── options/              # HTTP/GRPC/TLS 配置 + pflag
│   ├── registry/             # 服务注册中心抽象
│   │   ├── registry.go       # Registrar 接口 + ServiceInstance
│   │   ├── inmemory/         # 内存实现（开发/测试）
│   │   └── kratos.go         # 桥接 kratos registry（etcd/consul/nacos）
│   └── appkit/               # App 组合层（多 server 编排 + 注册/反注册）
└── cmd/bald/                 # 最小示例
```

## 快速开始

```go
package main

import (
    "context"
    "log/slog"
    "net/http"
    "os"
    "time"

    "github.com/spf13/pflag"
    "google.golang.org/grpc"

    baldlog "github.com/kalandramo/bald/pkg/log"
    "github.com/kalandramo/bald/pkg/appkit"
    baldoptions "github.com/kalandramo/bald/pkg/options"
    "github.com/kalandramo/bald/pkg/registry/inmemory"
    "github.com/kalandramo/bald/pkg/server"
)

func main() {
    // 1. 日志系统接入（进程入口 bootstrap 初始化全局 Logger）。
    //    通过 --log.level / --log.format / --log.output-paths 多源配置；
    //    FilterKey 脱敏：password/token 自动替换为 ***。
    logOpts := baldlog.NewOptions()
    logOpts.AddFlags(pflag.CommandLine)
    baldlog.SetLogger(baldlog.NewSlogLogger(logOpts,
        baldlog.WithFilter(baldlog.FilterKey("password")),
        baldlog.WithFilter(baldlog.FilterKey("token")),
        baldlog.WithAttrs(slog.String("service.name", "bald-demo")),
    ))
    logger := baldlog.GetLogger()

    // 2. 业务 options（SecureServing 内嵌 TLSOptions，Enabled=false 即明文 HTTP）。
    httpOpts := baldoptions.NewSecureServingOptions()
    httpOpts.Addr = ":8080"
    grpcOpts := baldoptions.NewGRPCOptions()
    // 注册 --bald-demo.http.addr / --bald-demo.http.tls.* 等 flag。
    httpOpts.AddFlags(pflag.CommandLine, baldoptions.Join("bald-demo", "http"))
    grpcOpts.AddFlags(pflag.CommandLine, baldoptions.Join("bald-demo", "grpc"))

    // 3. 共享 readiness 探针：HTTP /readyz 与 gRPC health 状态对称联动。
    ready := func(ctx context.Context) error { return nil /* 检查 DB/依赖 */ }

    httpSrv := server.NewHTTPServer(httpOpts, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _, _ = w.Write([]byte("hello from bald\n"))
    }), ready)
    grpcSrv := server.NewGRPCServerWithRegister(grpcOpts, nil, func(s *grpc.Server) {
        // pb.RegisterYourServer(s, impl)
    }, ready)

    app := appkit.New(
        appkit.Name("bald-demo"),
        appkit.Version("v0.1.0"),
        appkit.StopTimeout(15*time.Second),
        // 服务注册中心：开发/测试用 inmemory（零依赖、可真跑），
        // 生产用 appkit.KratosRegistrar(kr) 桥接 etcd/consul/nacos。
        appkit.Registrar(inmemory.New()),
        appkit.Servers(grpcSrv, httpSrv),
        appkit.AfterStart(func(ctx context.Context) error {
            // Endpoint() 在 Start 后返回真实地址（动态端口 :0 会解析为系统分配端口，
            // 通配符/空 host 会替换为首个可达 IP，确保其他节点可直连）。
            logger.Info(ctx, "bald-demo started",
                "grpc", grpcSrv.Endpoint(), "http", httpSrv.Endpoint())
            return nil
        }),
    )

    if err := app.Run(context.Background()); err != nil {
        logger.Error(context.Background(), "bald exited", "error", err)
        os.Exit(1)
    }
}
```

## Server 契约

```go
type Server interface {
    Start(ctx context.Context) error  // 阻塞直到 ctx 取消或出错
    Stop(ctx context.Context) error   // 优雅停止（ctx 携带停机超时）
    Endpoint() string                 // 实际监听地址（支持 :0 动态端口）
}
```

`Endpoint()` 在 `Start` 之后返回真实地址，便于注册到服务发现（如绑定 `:0` 时获取系统分配端口）。

## 生命周期与关键契约

`appkit.Run(ctx)` 行为（均有回归测试固化，见 `pkg/appkit/appkit_test.go`）：

| 契约 | 行为 |
|------|------|
| **并发启停** | errgroup 并发启动所有 Server |
| **BUG-1 停机超时** | `Stop` 接收**未取消**的 ctx（带 `StopTimeout`），超时配置才生效 |
| **BUG-3 崩溃级联** | 任一 Server `Start` 失败 → `gctx` 取消 → 其余 Server 级联停止 |
| **BUG-4 注册竞态** | `register` 前经 `waitForEndpoints` 轮询：各 Server `Endpoint()` 必须解析出真实端口（非 `:0`）且 `ln != nil`，超时 5s 报错，避免把 `xxx://:0` 注册到服务发现 |
| **Endpoint 可达性** | `Endpoint()` 经 `Extract` 把通配符/`0.0.0.0`/`::` 空 host 替换为首个全局单播 IP，显式 IP 原样保留；端口 `:0` 取 listener 实际端口，否则保留配置端口。仅配端口（`http://:8080`）时注册地址变为可达 IP（如 `http://10.66.50.21:8080`），其他节点可直连 |
| **防重入** | 重复 `Run` 返回 `ErrAlreadyRunning` |
| **可观察** | `Run` 结束后 `Done()` 关闭，`Err()` 返回退出错误 |
| **优雅停机顺序** | 先 `Deregister` 反注册，再 `stopAll` 优雅停机，避免流量打到已停服务 |

## 服务注册

默认无注册中心。可选接入：

```go
// 内存（开发/测试）
reg := inmemory.New()
appkit.New(appkit.Registrar(reg), appkit.Servers(srv))

// kratos 后端（etcd/consul/nacos）
import kratosEtcd "github.com/go-kratos/kratos/contrib/registry/etcd/v3"
kr := kratosEtcd.New(cli)
appkit.New(appkit.KratosRegistrar(kr), appkit.Servers(srv))
```

启动时 `Register`，停机时 `Deregister`；`Endpoints` 来自各 Server 的 `Endpoint()`（支持 `:0` 动态端口；通配符/空 host 自动解析为可达 IP）。

## 测试

```bash
go test ./...
```

回归测试覆盖：BUG-1 停机超时、BUG-3 崩溃级联、BUG-4 注册竞态、Run 防重入、可观察通道、注册/反注册、`:0` 动态端口与 endpoint 可达性（通配符/空 host 解析为可达 IP）。

## 设计文档

- 启动器（AppKit）完整设计（架构、契约、生命周期时序、配置加载、防坑要点、后续迭代清单）：
  [`docs/appkit-design.md`](docs/appkit-design.md)。
- 服务端设计（Server 契约、协议适配器、探针路由、health/reflection、Endpoint 可达性）：
  [`docs/server-design.md`](docs/server-design.md)。
- 服务注册设计（Registrar 抽象、ServiceInstance 字段约束、注册/反注册时序）：
  [`docs/registry-design.md`](docs/registry-design.md)。
- 配置中心设计（远程配置、多环境、`RemoteSource` 抽象、与 viper 集成）：
  [`docs/config-center-design.md`](docs/config-center-design.md)。
- 日志设计（Options 多源配置、FilterKey 脱敏、ContextWithAttrs 日志属性）：
  [`docs/log-design.md`](docs/log-design.md)。

## 可运行示例

最小可运行示例见 [`cmd/bald/main.go`](cmd/bald/main.go)，演示了：

- 本地 `--config` 文件 + 环境变量 + 命令行 flag 的优先级合并；
- 多环境（`--env=prod` 按 `bald-demo-prod.yaml` 选默认文件）；
- 本地文件热更新（`WatchConfigFile`）；
- 远程配置中心接入（etcd / nacos，通过 `config.FromKratosSource` 桥接，远程作基准、本地覆盖）；
- `OnConfigChange` 内热重载与 `BeforeStart` 取配置反序列化。

本地配置示例见 [`configs/bald-demo.yaml`](configs/bald-demo.yaml)。
