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
	baldoptions "github.com/kalandramo/bald/pkg/options"
	"github.com/kalandramo/bald/pkg/server"
)

func main() {
	httpOpts := baldoptions.NewSecureServingOptions()
	httpOpts.Addr = ":8080" // 明文 HTTP（Enabled 默认 false）
	grpcOpts := baldoptions.NewGRPCOptions()

	// 共享 readiness 探针：HTTP /readyz 与 gRPC health 状态对称联动。
	ready := func(ctx context.Context) error { return nil /* 检查 DB/依赖 */ }

	httpSrv := server.NewHTTPServer(httpOpts, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello from bald http\n"))
	}), ready)
	grpcSrv := server.NewGRPCServerWithRegister(grpcOpts, nil, func(s *grpc.Server) {
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

`BeforeStart` 钩子中从合并后的 `app.Viper()` 反序列化业务 options：

```go
appkit.BeforeStart(func(ctx context.Context) error {
	v := app.Viper()
	if v != nil {
		_ = v.Unmarshal(httpOpts)
		_ = v.Unmarshal(grpcOpts)
	}
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

仓库内置最小可运行示例 [`cmd/bald/main.go`](https://github.com/kalandramo/bald/blob/main/cmd/bald/main.go)，
演示了本地配置、环境变量、命令行 flag 与远程配置中心的接入方式。直接运行：

```bash
go run ./cmd/bald --config=configs/bald-demo.yaml
BALD_DEMO_HTTP_ADDR=:18080 go run ./cmd/bald        # 环境变量覆盖 http.addr
go run ./cmd/bald --http.addr=:18080                # flag 优先级最高
go run ./cmd/bald --env=prod                        # 多环境（按 bald-demo-prod.yaml）
```

### 下一步

- 阅读 [产品介绍](./introduction/README.md) 了解设计理念。
- 阅读 [开发手册](../devel/zh-CN/README.md) 深入 Server 契约、配置中心与服务注册设计。
