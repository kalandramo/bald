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
│   │   ├── http_server.go    # net/http（支持 HTTP/HTTPS，解析动态端口）
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
    "log"
    "net/http"
    "time"

    "context"

    "google.golang.org/grpc"

    "github.com/kalandramo/bald/pkg/appkit"
    "github.com/kalandramo/bald/pkg/options"
    "github.com/kalandramo/bald/pkg/server"
)

func main() {
    httpOpts := options.NewHTTPOptions()
    grpcOpts := options.NewGRPCOptions()

    // 共享 readiness 探针：HTTP /readyz 与 gRPC health 状态对称联动。
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
        // appkit.Registrar(reg),        // 可选：内存/etcd/consul
        // appkit.KratosRegistrar(kr),   // 可选：桥接 kratos 后端
        appkit.Servers(grpcSrv, httpSrv),
        appkit.AfterStart(func(ctx context.Context) error {
            log.Printf("started: grpc=%s http=%s", grpcSrv.Endpoint(), httpSrv.Endpoint())
            return nil
        }),
    )

    if err := app.Run(context.Background()); err != nil {
        log.Fatalf("bald exited: %v", err)
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

启动时 `Register`，停机时 `Deregister`；`Endpoints` 来自各 Server 的 `Endpoint()`（支持 `:0` 动态端口）。

## 测试

```bash
go test ./...
```

回归测试覆盖：BUG-1 停机超时、BUG-3 崩溃级联、Run 防重入、可观察通道、注册/反注册。

## 设计文档

- 启动器（AppKit）完整设计（架构、契约、生命周期时序、配置加载、防坑要点、后续迭代清单）：
  [`docs/appkit-design.md`](docs/appkit-design.md)。
- 配置中心设计（远程配置、多环境、`RemoteSource` 抽象、与 viper 集成）：
  [`docs/config-center-design.md`](docs/config-center-design.md)。

## 可运行示例

最小可运行示例见 [`cmd/bald/main.go`](cmd/bald/main.go)，演示了：

- 本地 `--config` 文件 + 环境变量 + 命令行 flag 的优先级合并；
- 多环境（`--env=prod` 按 `bald-demo-prod.yaml` 选默认文件）；
- 本地文件热更新（`WatchConfigFile`）；
- 远程配置中心接入（etcd / nacos，通过 `config.FromKratosSource` 桥接，远程作基准、本地覆盖）；
- `OnConfigChange` 内热重载与 `BeforeStart` 取配置反序列化。

本地配置示例见 [`configs/bald-demo.yaml`](configs/bald-demo.yaml)。
