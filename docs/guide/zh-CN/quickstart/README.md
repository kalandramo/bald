## 快速入门

本指南带你用 bald 在几分钟内跑起一个同时提供 HTTP 与 gRPC 的服务。

### 1. 环境要求

- Go 1.26+（见 `go.mod` 中 `go 1.26.5`）
- 一个 Go module 项目

### 2. 获取依赖

在项目中引入 bald：

```bash
go get github.com/kalandramo/bald
```

### 3. 最小可运行示例

下面是一份最小 `main.go`，演示用 `AppKit` 组合一个 HTTP 服务与一个 gRPC 服务：

```go
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/pkg/appkit"
	baldconf "github.com/kalandramo/bald/pkg/conf"
	"github.com/kalandramo/bald/pkg/server"
)

func main() {
	// 框架级配置：proto 是唯一真相源，server 直接消费 Bootstrap 子消息。
	bootstrap := baldconf.NewBootstrap()
	bootstrap.Http.Addr = ":8080" // 明文 HTTP（Tls.Enabled 默认 false）

	// 共享 readiness 探针：HTTP /readyz 与 gRPC health 状态对称联动。
	ready := func(ctx context.Context) error { return nil /* 检查 DB/依赖 */ }

	httpSrv := server.NewHTTPServer(bootstrap.GetHttp(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello from bald http\n"))
	}), ready)
	grpcSrv := server.NewGRPCServerWithRegister(bootstrap.GetGrpc(), nil, func(s *grpc.Server) {
		// pb.RegisterYourServer(s, impl)
	}, ready)

	app := appkit.New(
		appkit.Name("bald-demo"),
		appkit.Version("v0.1.0"),
		appkit.StopTimeout(15*time.Second),
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

运行：

```bash
go run ./main.go
```

启动后日志会打印 `grpc=` / `http=` 的真实监听地址。

### 4. 配置加载（可选）

bald 采用多源配置，优先级（高 → 低）：

```
命令行 flag  >  环境变量  >  本地文件  >  远程配置中心
```

在 `AppKit` 上通过 Option 启用：

```go
appkit.ConfigFile("configs/bald-demo.yaml"), // 本地配置文件
appkit.WatchConfigFile(true),               // 文件变更热更新
appkit.OnConfigChange(func(v *viper.Viper) { /* 热重载 */ }),
// appkit.RemoteConfig(src),                // 可选：etcd/consul/nacos 远程配置
```

`BeforeStart` 钩子中按 proto 契约解析（proto 是唯一真相源，`pkg/options` 中间层已废弃删除）：

```go
appkit.BeforeStart(func(ctx context.Context) error {
    if err := baldconfig.Unmarshal(app.Viper(), bootstrap); err != nil {
        return fmt.Errorf("unmarshal config: %w", err)
    }
    if err := baldconf.Validate(bootstrap); err != nil {
        return fmt.Errorf("invalid config: %w", err)
    }
    // server 已持有 bootstrap.GetHttp()/GetGrpc() 指针，无需回填。
    return nil
}),
```

本地配置示例（`configs/bald-demo.yaml`）：

```yaml
http:
  addr: ":8080"
grpc:
  addr: ":9090"
```

### 5. 接入服务注册（可选）

默认无注册中心。需要服务发现时注入：

```go
// 内存（开发/测试）
import "github.com/kalandramo/bald/pkg/registry/inmemory"
reg := inmemory.New()
appkit.New(appkit.Registrar(reg), appkit.Servers(srv))

// kratos 后端（etcd/consul/nacos）
import kratosEtcd "github.com/go-kratos/kratos/contrib/registry/etcd/v3"
kr := kratosEtcd.New(cli)
appkit.New(appkit.KratosRegistrar(kr), appkit.Servers(srv))
```

启动时自动 `Register`，停机时自动 `Deregister`；`Endpoints` 来自各 Server 的
`Endpoint()`，支持 `:0` 动态端口。

### 6. 完整示例

仓库内置完整可运行示例 [`_example/bald/main.go`](https://github.com/kalandramo/bald/blob/main/_example/bald/main.go)，
覆盖框架的核心能力：

- **多协议编排**：并发启停 HTTP + gRPC 两个 `server.Server`，共享同一个 `ReadinessFunc` 使 `/readyz` 与 gRPC health 对称联动。
- **配置四源合并**：本地文件（`--config`）+ 环境变量 + 命令行 flag + 可选远程配置中心，优先级 `flag > 环境变量 > 本地文件 > 远程`；并演示 `WatchConfigFile` 热更新与 `OnConfigChange` 回调回填业务 options。
- **日志系统接入**：进程入口用 `baldlog.SetLogger(baldlog.NewSlogLogger(...))` 初始化全局 `Logger`，经 `--log.level` / `--log.format` / `--log.output-paths` 多源配置；内置 `FilterKey` 脱敏（如 `password`/`token` 自动替换为 `***`）；框架与业务统一通过 `log.GetLogger()` 取同一实例。
- **服务注册中心**：通过 `appkit.Registrar(inmemory.New())` 端到端演示 register → 运行 → deregister 全流程（零外部依赖），并验证 `:0` 动态端口聚合注册（真实 Endpoint 解析后才注册，避免注册 `xxx://:0`）。生产环境改用 `appkit.KratosRegistrar(kr)` 桥接 etcd/consul/nacos。
- **上下文属性流**：`AfterStart` 中 `log.ContextWithAttrs(ctx, ...)` 挂载的属性，会在该 ctx 范围内的日志自动携带。

直接运行：

```bash
go run ./_example/bald --config=configs/bald-demo.yaml
BALD_DEMO_HTTP_ADDR=:18080 go run ./_example/bald        # 环境变量覆盖 http.addr
go run ./_example/bald --http.addr=:18080                # flag 优先级最高
go run ./_example/bald --env=prod                        # 多环境（按 bald-demo-prod.yaml）
go run ./_example/bald --log.format=json --log.level=debug   # 切换日志格式 / 级别
```

> 注：`_example/bald/main.go` 顶部注释还给出了 etcd/nacos 远程配置中心与 GatewayServer、单 server `Serve()` 的接入片段，可按需启用。

### 下一步

- 阅读 [产品介绍](./introduction/README.md) 了解设计理念。
- 阅读 [开发手册](../devel/zh-CN/README.md) 深入 Server 契约、配置中心与服务注册设计。
