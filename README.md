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
│   ├── web/                 # 强绑定 gin 的「绑定/校验/响应」流水线（依赖 gin + pkg/berrors）
│   │   ├── handler.go        # 泛型 HandleJSON/URI/Query/AllRequest：绑定→校验→响应（吃 *gin.Context）
│   │   ├── binder.go         # ShouldBindAll：URI>Query>JSON 依次覆盖 + Defaulter 默认填充
│   │   └── response.go       # 统一响应 + 错误→HTTP 状态码映射（pkg/berrors）+ ErrorBody
│   ├── middleware/           # HTTP/gRPC 中间件（统一由装配层注入；业务侧直接用 pkg/middleware/gin）
│   │   ├── gin/              # Recovery / RequestID / CORS / Secure / Logging / Authn / Authz / Observability
│   │   └── grpc/             # gRPC 拦截器（对应 gin 中间件能力）
│   ├── validation/           # 类型化校验（反射式 Validate<ReqType> 按类型名分发；也支持 Rules）
│   │   ├── validation.go     # Validator 注册与按请求类型分发
│   │   └── validator.go      # 字段级 Rules 校验工具
│   ├── errors/               # 零依赖错误模型（WindError）
│   │   ├── errors.go         # Error + Is/As/Unwrap + 不可变 builder
│   │   ├── code.go           # 与 gRPC codes 1:1 的 HTTP 状态码常量
│   │   ├── http.go           # 业务错误构造器（BadRequest/NotFound/...）
│   │   └── grpcerr/          # 可选 gRPC 桥接 ToStatus/FromStatus
│   ├── config/               # 配置抽象（RemoteSource / 编码器 / 多源合并）
│   │   ├── config.go         # 配置中心核心
│   │   ├── source.go         # RemoteSource 抽象（etcd/nacos 桥接）
│   │   └── log/              # 配置相关日志辅助
│   ├── contextx/             # 上下文辅助（请求属性注入/提取）
│   ├── options/              # HTTP/GRPC/TLS 配置 + pflag
│   ├── registry/             # 服务注册中心抽象
│   │   ├── registry.go       # Registrar 接口 + ServiceInstance
│   │   ├── inmemory/         # 内存实现（开发/测试）
│   │   └── kratos.go         # 桥接 kratos registry（etcd/consul/nacos）
│   └── appkit/               # App 组合层（多 server 编排 + 注册/反注册）
└── _example/bald/            # 最小示例（含 bald-gin 纯 gin 引擎对照、appkit 编排 HTTP/gRPC 双协议）
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
    baldconf "github.com/kalandramo/bald/pkg/conf"
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

    // 2. 框架级配置：proto 是唯一真相源，server 直接消费 Bootstrap 子消息。
    bootstrap := baldconf.NewBootstrap()
    bootstrap.Http.Addr = ":8080"
    // 注册 --bald-demo.http.addr / --bald-demo.http.tls.* 等 flag（BindFlags 遍历 proto 字段）。
    baldconf.BindFlags(pflag.CommandLine, bootstrap.GetHttp(), "bald-demo.http")
    baldconf.BindFlags(pflag.CommandLine, bootstrap.GetGrpc(), "bald-demo.grpc")

    // 3. 共享 readiness 探针：HTTP /readyz 与 gRPC health 状态对称联动。
    ready := func(ctx context.Context) error { return nil /* 检查 DB/依赖 */ }

    httpSrv := server.NewHTTPServer(bootstrap.GetHttp(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _, _ = w.Write([]byte("hello from bald\n"))
    }), ready)
    grpcSrv := server.NewGRPCServerWithRegister(bootstrap.GetGrpc(), nil, func(s *grpc.Server) {
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

## Web 框架（pkg/web 强绑定 gin）

**分层原则**：路由注册由业务直接用 gin 编写；「绑定 / 校验 / 响应」流水线集中在 `pkg/web`——它**强绑定 gin**（直接消费 `*gin.Context`），因此 bald 仅支持 **gin（HTTP）与 grpc-gateway（复用同一 biz 层）**。这与 onexstack 的 `pkg/core` 思路一致，但错误语义复用 bald 自有的 `pkg/berrors`（不引入 onexstack 外部依赖）。

`gin.Engine`（或任意 `http.Handler`）可直接传给 `server.NewHTTPServer`，**服务器层零改动**。

### 路由与中间件

```go
// 常用中间件直接用 pkg/middleware/gin（实现就在此包，无再导出层）。
router := gin.New()
router.Use(mid.Recovery(), mid.RequestID(), mid.Logging())

// 业务自行注册路由（路由归属模块，认证等中间件由装配层注入）：
router.GET("/v1/articles/:id", func(c *gin.Context) {
    web.HandleUriRequest[struct{ ID string `json:"id" uri:"id"` }, map[string]string](
        c,
        func(_ context.Context, a *struct{ ID string `json:"id" uri:"id"` }) (map[string]string, error) {
            return map[string]string{"id": a.ID}, nil
        })
})

httpSrv := server.NewHTTPServer(httpOpts, router, ready)
```

路径参数由 gin 原生 `ShouldBindUri` 解析，结构体字段用 `uri` tag 标注（与路径变量名一致），**无需任何桥接层**。

HTTP 中间件（Recovery/RequestID/CORS/Secure/Logging/Authn/Authz/Observability）统一放在 `pkg/middleware/gin` 与 `pkg/middleware/grpc`，由业务装配层注入。

### 请求绑定与响应（pkg/web）

强绑定 gin 的泛型流水线把"绑定 → 默认值 → 校验 → 业务 → 响应"焊成一条，实现位于 `pkg/web`：`HandleAllRequest`（URI > Query > JSON，后者覆盖）、`HandleJSONRequest`、`HandleQueryRequest`、`HandleUriRequest`。入口函数直接吃 `*gin.Context`，Handler 签名为指针风格 `func(ctx, *T) (R, error)`，与 onexstack 完全一致。

```go
type articleReq struct {
    ID    string `json:"id" uri:"id"` // URI 路径变量
    Lang  string `json:"lang"`        // Query（?lang=zh）
    Title string `json:"title"`       // JSON body
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
        a.Lang = "en" // 默认值：建议在 Defaulter.Default() 中填充，这里只做校验
    }
    return nil
}

web.HandleAllRequest[articleReq, articleResp](c,
    func(_ context.Context, a *articleReq) (articleResp, error) {
        return articleResp{ID: a.ID, Lang: a.Lang, Title: a.Title}, nil
    },
    validate)
```

- **绑定源**：URI（路径变量，需 `uri` tag）→ Query → JSON body，按顺序覆盖，最后统一校验，避免部分填充误报必填。
- **统一响应**：成功写 `data`（JSON），失败写 `ErrorBody{error:{code, message}}`。

### 错误 → HTTP 状态码映射

`web.ErrorResponse` 与错误模型通过 `pkg/berrors` 衔接：

```go
// pkg/berrors 的错误经 httperr 子包还原状态码（核心包不挂 StatusCode 方法，避免循环依赖）：
werr, ok := errors.FromError(err)   // 拆链还原为 *errors.Error
status := httperr.StatusCode(werr)  // 由 Code（gRPC code）映射到 HTTP 状态码
```

- 能被 `errors.FromError` 还原的错误取 `httperr.StatusCode()`；无法还原的统一 `500`。
- `pkg/berrors.Error` 通过 `WithCause` 包装底层错误，`FromError` 会**拆链**命中正确状态码，被 `fmt.Errorf("wrap: %w", err)` 包裹的错误也不会误落 500。

## 错误模型（pkg/berrors）

零依赖（仅标准库）的 `berrors.Error`：传输中立的错误模型，跨服务边界只做"转换"，不绑定具体协议（gRPC 映射在 `grpcerr`、HTTP 映射在 `httperr` 两个边界子包）。

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

状态码常量见 `pkg/berrors/code.go`（如 `CodeBadRequest=400`、`CodeNotFound=404`、`CodeInternal=500`），与 gRPC `codes.Code` 一一对应。

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
- 路由注册与绑定设计（Router 分组/中间件链、多源绑定顺序、泛型流水线、统一响应、pkg/berrors 契约）：
  [`docs/devel/zh-CN/路由注册与绑定设计.md`](docs/devel/zh-CN/路由注册与绑定设计.md)。
- 错误模型设计（WindError 字段、代码-原因分离、不可变 builder、HTTP/gRPC 双栈桥接）：
  [`docs/devel/zh-CN/错误模型设计.md`](docs/devel/zh-CN/错误模型设计.md)。

## 可运行示例

最小可运行示例见 [`example/bald/main.go`](example/bald/main.go)，演示了：

- 本地 `--config` 文件 + 环境变量 + 命令行 flag 的优先级合并；
- 多环境（`--env=prod` 按 `bald-demo-prod.yaml` 选默认文件）；
- 本地文件热更新（`WatchConfigFile`）；
- 远程配置中心接入（etcd / nacos，通过 `config.FromKratosSource` 桥接，远程作基准、本地覆盖）；
- `OnConfigChange` 内热重载与 `BeforeStart` 取配置反序列化；
- **路由**：业务直接用 `gin.Engine` 经 `server.NewHTTPServer` 挂载，自行注册路由（含 CORS 中间件、URI/JSON/多源绑定、`HandleAllRequest` 校验器）；
- **`pkg/berrors` 错误**：业务 handler 用 `berrors.BadRequest(...).WithMessage(...)` 返回结构化错误，由 `web` 的 `ErrorResponse` 自动映射 HTTP 状态码与 `ErrorBody`。

示例路由（服务起来后可直接 curl 验证）：

```bash
curl -i http://127.0.0.1:8080/v1/ping
curl -i -XPOST http://127.0.0.1:8080/v1/greet \
  -H "Content-Type: application/json" -d '{"name":"bald"}'
curl -i -XPOST 'http://127.0.0.1:8080/v1/articles/42?lang=zh' \
  -H "Content-Type: application/json" -d '{"title":"hi"}'
```

> **Windows PowerShell 等效命令**（已验证可用，直接复制执行）：

```powershell
curl.exe -i http://127.0.0.1:8080/v1/ping

curl.exe --% -i -XPOST http://127.0.0.1:8080/v1/greet -H "Content-Type: application/json" -d "{\"name\":\"bald\"}"

curl.exe --% -XPOST "http://127.0.0.1:8080/v1/articles/42?lang=zh" -H "Content-Type: application/json" -d "{\"title\":\"hi\"}"
```

> 说明：PowerShell 下用 `curl.exe` 避开 `curl` 别名指向 `Invoke-WebRequest`。**关键是 `--%`
> （stop-parsing 标记）**：`--%` 之后的内容 PowerShell 完全不再解析、原样传给 `curl.exe`，
> 否则 PowerShell 会把单引号里的 `{` 当作脚本块/元字符处理掉，导致 JSON 首字符丢失，触发
> 若 JSON 体非法，会返回 `400` 且 Reason 为绑定错误信息（如 `invalid character 'n' looking for beginning of object key string`）。`--%`
> 之后 JSON 内双引号以 `\"` 转义（Windows curl 标准写法）。**务必带
> `-H "Content-Type: application/json"`**，否则同样触发 400（bind 错误）。

> **发送 JSON body 必须带 `-H "Content-Type: application/json"`**：`curl -d` 默认把
> Content-Type 设为 `application/x-www-form-urlencoded`。`pkg/web` 的 JSON 绑定器**只解析
> `application/json` 类型**，对缺失/非 JSON 的 Content-Type 且携带 body 的请求，按"非法即
> 拒绝"原则**直接返回 400 + 结构化 `ErrorBody{error:{code, message}}`**（code 为绑定错误信息），而不是静默跳过把
> 锅甩给业务校验。因此发 JSON 时务必显式带该头。上述命令在 Git Bash / WSL / macOS / cmd 下均
> 可用；**Windows PowerShell 必须用 `--%` 写法**（见上），单引号包裹 JSON 在 PowerShell 调用
> 外部 exe 时会因 `{}` 被当脚本块解析而失败。


本地配置示例见 [`configs/bald-demo.yaml`](configs/bald-demo.yaml)。
