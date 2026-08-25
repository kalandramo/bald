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
│   ├── web/                  # 标准库 net/http 之上的轻量 Web 框架
│   │   ├── router.go         # Router（实现 http.Handler）+ 分组 + 中间件链
│   │   ├── binding.go        # 多源绑定 URI > Query > JSON，统一校验后返回
│   │   ├── request.go        # 泛型流水线 HandleAllRequest / JSON / Query / URI
│   │   ├── response.go       # 统一响应 + 错误→HTTP 状态码映射（StatusCoder）
│   │   └── middleware.go     # Recovery / RequestID / CORS / Secure / Logging
│   ├── errors/               # 零依赖错误模型（WindError）
│   │   ├── errors.go         # Error + Is/As/Unwrap + 不可变 builder
│   │   ├── code.go           # 与 gRPC codes 1:1 的 HTTP 状态码常量
│   │   ├── http.go           # 业务错误构造器（BadRequest/NotFound/...）
│   │   └── grpcerr/          # 可选 gRPC 桥接 ToStatus/FromStatus
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

## Web 框架（pkg/web）

基于标准库 `net/http` 的极薄封装：`Router` 实现 `http.Handler`，可直接传给 `server.NewHTTPServer`，**服务器层零改动**。只额外提供两样 `http.ServeMux` 没有的东西：分组（Group）与中间件链。

### 路由与中间件

```go
router := web.NewRouter(web.Recovery(), web.RequestID(), web.Logging())

// 业务模块自挂载：认证等中间件由装配层注入，路由归属模块。
type articleHandler struct{}
func (articleHandler) Name() string { return "article" }
func (articleHandler) ApplyTo(r *web.Router, mw ...web.Middleware) error {
    v1 := r.Group("/v1", append([]web.Middleware{web.CORS(web.DefaultCORS())}, mw...)...)
    v1.HandleFunc("GET", "/articles/{id}", func(w http.ResponseWriter, req *http.Request) {
        web.HandleUriRequest[struct{ ID string `json:"id"` }, map[string]string](
            w, req,
            func(_ context.Context, a *struct{ ID string `json:"id"` }) (map[string]string, error) {
                return map[string]string{"id": a.ID}, nil
            })
    })
    return nil
}

_ = articleHandler{}.ApplyTo(router)
httpSrv := server.NewHTTPServer(httpOpts, router, ready)
```

中间件顺序（由外到内）：根路由器 → 子组（Group）→ 路由级。内置 `Recovery` / `RequestID` / `CORS` / `Secure` / `Logging` 可直接组合。

### 请求绑定与响应

泛型流水线把"绑定 → 默认值 → 校验 → 业务 → 响应"焊成一条：`HandleAllRequest`（URI > Query > JSON，后者覆盖）、`HandleJSONRequest`、`HandleQueryRequest`、`HandleUriRequest`。

```go
type articleReq struct {
    ID    string `json:"id"`    // URI 路径变量
    Lang  string `json:"lang"`  // Query（?lang=zh）
    Title string `json:"title"` // JSON body
}
type articleResp struct {
    ID    string `json:"id"`
    Lang  string `json:"lang"`
    Title string `json:"title"`
}

validate := func(_ context.Context, a *articleReq) error {
    if a.Title == "" {
        return berrors.BadRequest("EMPTY_TITLE").WithMessage("title 不能为空")
    }
    if a.Lang == "" {
        a.Lang = "en" // 默认值在校验阶段修正
    }
    return nil
}

web.HandleAllRequest[articleReq, articleResp](w, req,
    func(_ context.Context, a *articleReq) (articleResp, error) {
        return articleResp{ID: a.ID, Lang: a.Lang, Title: a.Title}, nil
    },
    validate)
```

- **绑定源**：URI（路径变量，字段名需与 `{var}` 一致）→ Query → JSON body，按顺序覆盖，最后统一校验，避免部分填充误报必填。
- **统一响应**：成功写 `data`（JSON），失败写 `ErrorResponse{reason, message, metadata}`。

### 错误 → HTTP 状态码映射

`web.WriteResponse` 与错误模型的唯一契约是 `StatusCoder`：

```go
type StatusCoder interface { StatusCode() int }
```

- 实现了 `StatusCoder` 的错误取 `StatusCode()`；未实现的统一 `500`。
- `pkg/errors.Error` 已实现该方法，且 `writeError` 通过 `errors.As` **拆链**——被 `fmt.Errorf("wrap: %w", err)` 包裹的错误也能命中正确状态码，不会误落 500。

## 错误模型（pkg/errors）

零依赖（仅标准库）的 `WindError`：传输中立的错误模型，跨服务边界只做"转换"，不绑定具体协议。

```go
// 业务错误：代码-原因-消息三者分离。
err := berrors.NotFound("ORDER_NOT_FOUND").
    WithMessage("订单不存在").
    WithDetails(map[string]string{"order_id": "123"}).
    WithCause(dbErr) // 包装底层错误，保留链路

// 按稳定 Reason 匹配（与 gRPC codes 1:1 的 HTTP 状态码自动映射）。
if berrors.Is(err, berrors.ErrNotFound) { /* ... */ }

// gRPC 桥接（可选）：HTTP/gRPC 双栈直连。
st := grpcerr.ToStatus(err)            // *status.Status
back := grpcerr.FromStatus(st)         // *berrors.Error
```

核心特性：

| 特性 | 说明 |
|------|------|
| **代码-原因分离** | `Code`（HTTP 状态码，与 gRPC `codes.Code` 1:1，但零 gRPC 依赖）、`Reason`（稳定可枚举标识，如 `ORDER_NOT_FOUND`，`Is` 按它匹配）、`Message`（给人看）、`Details`（i18n 动态变量） |
| **不可变 builder** | `WithMessage` / `WithCause` / `WithDetails` 返回新实例，原哨兵（sentinel）不被污染，可安全并发共享 |
| **错误链完整** | 内嵌 `cause` + 调用栈，`errors.Is/As/Unwrap` 原生可用 |
| **HTTP/gRPC 双栈** | `http.go` 提供 `BadRequest/Unauthorized/NotFound/...` 构造器；`grpcerr` 子包做 `ToStatus/FromStatus` 桥接 |

状态码常量见 `pkg/errors/code.go`（如 `CodeBadRequest=400`、`CodeNotFound=404`、`CodeInternal=500`），与 gRPC `codes.Code` 一一对应。

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
- 路由注册与绑定设计（Router 分组/中间件链、多源绑定顺序、泛型流水线、统一响应、StatusCoder 契约）：
  [`docs/devel/zh-CN/路由注册与绑定设计.md`](docs/devel/zh-CN/路由注册与绑定设计.md)。
- 错误模型设计（WindError 字段、代码-原因分离、不可变 builder、HTTP/gRPC 双栈桥接）：
  [`docs/devel/zh-CN/错误模型设计.md`](docs/devel/zh-CN/错误模型设计.md)。

## 可运行示例

最小可运行示例见 [`cmd/bald/main.go`](cmd/bald/main.go)，演示了：

- 本地 `--config` 文件 + 环境变量 + 命令行 flag 的优先级合并；
- 多环境（`--env=prod` 按 `bald-demo-prod.yaml` 选默认文件）；
- 本地文件热更新（`WatchConfigFile`）；
- 远程配置中心接入（etcd / nacos，通过 `config.FromKratosSource` 桥接，远程作基准、本地覆盖）；
- `OnConfigChange` 内热重载与 `BeforeStart` 取配置反序列化；
- **`pkg/web` 路由**：`web.Router` 经 `server.NewHTTPServer` 挂载，业务模块用 `ApplyTo` 自挂载路由（含 CORS 中间件、URI/JSON/多源绑定、`HandleAllRequest` 校验器）；
- **`pkg/errors` 错误**：业务 handler 用 `berrors.BadRequest(...).WithMessage(...)` 返回结构化错误，由 `web.WriteResponse` 自动映射 HTTP 状态码与 `ErrorResponse`。

示例路由（服务起来后可直接 curl 验证）：

```bash
curl -i http://127.0.0.1:8080/v1/ping
curl -i -XPOST http://127.0.0.1:8080/v1/greet \
  -H "Content-Type: application/json" -d '{"name":"bald"}'
curl -i -XPOST 'http://127.0.0.1:8080/v1/articles/42?lang=zh' \
  -H "Content-Type: application/json" -d '{"title":"hi"}'
```

> **Windows PowerShell 等效命令**（直接可复制执行）：

```powershell
curl.exe -i http://127.0.0.1:8080/v1/ping

curl.exe -i -XPOST http://127.0.0.1:8080/v1/greet `
  -H "Content-Type: application/json" -d '{"name":"bald"}'

curl.exe -i -XPOST 'http://127.0.0.1:8080/v1/articles/42?lang=zh' `
  -H "Content-Type: application/json" -d '{"title":"hi"}'
```

> 说明：PowerShell 下用 `curl.exe` 避开 `curl` 别名指向 `Invoke-WebRequest`；多行续行符是
> 反引号 `` ` ``（行尾）；`-d` 的 JSON 用单引号包裹（PowerShell 单引号是合法字面量定界符且不
> 展开变量）。务必带 `-H "Content-Type: application/json"`，否则触发 400 `BIND_ERROR`。

> **发送 JSON body 必须带 `-H "Content-Type: application/json"`**：`curl -d` 默认把
> Content-Type 设为 `application/x-www-form-urlencoded`。`pkg/web` 的 JSON 绑定器**只解析
> `application/json` 类型**，对缺失/非 JSON 的 Content-Type 且携带 body 的请求，按"非法即
> 拒绝"原则**直接返回 400 + 结构化 `ErrorResponse{reason:"BIND_ERROR"}`**，而不是静默跳过把
> 锅甩给业务校验。因此发 JSON 时务必显式带该头。上述命令在 Git Bash / WSL / PowerShell /
> cmd 下均可用。Windows 若用 PowerShell 原生 curl，注意用 `curl.exe` 避开 `curl` 别名指向
> `Invoke-WebRequest`；单引号在 PowerShell 中是合法字符串定界符。


本地配置示例见 [`configs/bald-demo.yaml`](configs/bald-demo.yaml)。
