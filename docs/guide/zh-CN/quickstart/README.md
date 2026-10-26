## 快速入门

本指南带你用一条生成命令跑起一个同时提供 HTTP 与 gRPC 的最小 bald 服务：声明一份 AppSpec → `bald gen app` 生成装配骨架 → 起服务 → 验证探针与业务端点 → 感受配置契约校验。字段的完整参考、加域、加 proto、授权接线见[用 bald CLI 起步新服务](../用%20bald%20CLI%20起步新服务.md)。

### 1. 环境要求

- Go 1.27+（bald `go.mod` 声明 `go 1.27.1`）
- 一个 Go module 项目（下文假定已执行 `go mod init github.com/you/demo-svc`，按需替换）
- 官方 CLI（一次性安装；业务代码只依赖 bald 库）：

```bash
go install github.com/kalandramo/bald/cmd/bald@v0.25.0
```

### 2. 声明 + 一条命令生成

bald 的起步方式是「先声明、再生成」：AppSpec（JSON）声明应用开通哪些传输面，CLI 据此生成整套可编译的装配骨架。最小声明只需要 `meta` 与 `server`：

```json
{
  "meta": {
    "name": "demo-svc",
    "module": "github.com/you/demo-svc",
    "desc": "我的第一个 bald 服务"
  },
  "server": { "http": true, "grpc": true, "httpAddr": ":8080", "grpcAddr": ":9090" }
}
```

在 module 根目录生成：

```bash
bald gen app --spec appspec.json
```

### 3. 生成物树解读：谁重写、谁生成一次、谁归你

```text
demo-svc/
├─ appspec.json                                    # 用户物：声明真相源，生成器永不读写
├─ cmd/demo-svc/main.go                            # 生成物：≤25 行，装配知识为零
├─ cmd/demo-svc/app/command.go                     # 生成物：cobra 根 + 唯一 MustServe 调用
├─ internal/bootstrap/assemble.gen.go              # 生成物：Options() 是装配的全部
├─ internal/bootstrap/domains.gen.go               # 生成物：业务域清单（暂空，占位）
├─ internal/bootstrap/seed/seed.go                 # 生成一次：演示种子数据
├─ internal/bootstrap/seed/policies.go             # 生成一次：默认授权策略行
├─ internal/apiserver/apiserver.go                 # 生成一次：HTTP 装配钩子（加业务路由处）
├─ internal/apiserver/domains.gen.go               # 生成物：域声明对账清单
├─ internal/apiserver/layering_invariant_test.go   # 生成物：biz 层不 import 装配层的守卫测试
├─ api/buf.yaml / api/buf.gen.yaml                 # 生成物：buf 模块与生成配置
├─ configs/demo-svc.yaml                           # 生成物：契约配置（由 AppSpec 派生）
└─ taskfile.yml                                    # 生成物：task run / verify / proto
```

归属三态（决定重新生成时发生什么）：

- **生成物**（文件头标 `DO NOT EDIT`）：内容以生成器为准，重新生成时重写；已存在时需 `--force` 放行。
- **生成一次**（`internal/bootstrap/seed/`、`internal/apiserver/apiserver.go`）：只在该文件不存在时落盘，重生成输出 `kept:` 跳过（`--force` 也不覆盖）——此后归你维护。
- **用户物**：`appspec.json` 与你的业务代码，生成器永不读写。

入口链只有三跳：`main.go`（行数硬预算 ≤25）→ `app.Execute()`（cobra 根）→ `appkit.MustServe(context.Background(), bootstrap.Options()...)`。装配知识全部在 `internal/bootstrap/assemble.gen.go` 的 `Options()`：`std.Standard()`（server/audit/logger 三类框架自有后端的段驱动 Provider）+ `ConfigFile`/`WatchConfigFile` + `appkit.StrictContract()`（生成物默认开启，见 §6）+ 传输面能力声明（`WithHTTP`/`WithGRPC`）+ 探针装配。地址、日志、TLS 等可配置项不写死在代码里——由 `configs/demo-svc.yaml` 按契约键驱动。

### 4. 拉依赖、起服务

```bash
go get github.com/kalandramo/bald@v0.25.0
go mod tidy
go run ./cmd/demo-svc
```

启动日志由引擎打印（`appkit starting` → `appkit started`；声明组件后还会出现 `appkit component started`）。验证探针（框架默认挂载 `/healthz` 与 `/readyz`，数据源是生成物里的 `healthChecker`）：

```bash
curl -i http://127.0.0.1:8080/healthz   # 200：存活
curl -i http://127.0.0.1:8080/readyz    # 200：就绪（依赖未就绪 503，K8s 摘流量）
```

### 5. 加第一个业务端点

生成的树不含业务路由——HTTP 装配钩子是 `internal/apiserver/apiserver.go`（生成一次、归你维护）。在 `NewRouter` 的 `return router` 之前加一条路由：

```go
router.GET("/v1/hello", func(c *gin.Context) {
    _, _ = c.Writer.Write([]byte("hello from bald\n"))
})
```

重启后验证：

```bash
curl -i http://127.0.0.1:8080/v1/hello
```

更完整的路由写法（路由分组、中间件链、结构化绑定与校验、错误到 HTTP 状态码的映射）直接照抄示例 [`_example/bald/internal/apiserver/router.go`](https://github.com/kalandramo/bald/blob/main/_example/bald/internal/apiserver/router.go) 的 `exampleRoutes`。

### 6. 加一个配置项：契约校验当场拦截

配置键即 proto 契约的字段路径（键名口径详见[Bald 配置系统](../Bald%20配置系统.md)）。把 HTTP 地址改到 18080——编辑 `configs/demo-svc.yaml`：

```yaml
server:
  http:
    addr: ":18080"
```

重启生效；也可以不动文件，用覆盖源：flag `--server.http.addr=:18080`（优先级最高）或环境变量 `DEMO_SVC_SERVER_HTTP_ADDR=:18080`（前缀由契约 `app.name` 派生，生成物已把 `meta.name` 写进该键）。

再故意放一个契约里没有的键进去（比如业务参数误落在契约段下）：

```yaml
server:
  http:
    addr: ":18080"
    greeting: "hi"   # server.http 下没有这个键
```

启动即失败，报错列出全部未知键：

```text
config: unknown contract keys (typo? or declare in AllowBusinessSections): server.http.greeting
```

这是生成物默认 emit 的 `appkit.StrictContract()` 在起作用：未知键（含 env/flag 来源的笔误）启动期 fail-fast，而不是静默落零值。手写应用建议显式加上这个 Option；业务自持的顶层段（不属于契约的段）经 `appkit.StrictContract("<业务段名>")` 整棵放行。配置文件热更新已随 `WatchConfigFile(true)` 开启，热更回调形态是 `appkit.OnConfigChange(func(map[string]any))`（形参为重新合并后的配置快照）。

### 7. 可选：服务注册中心

默认无注册中心。需要服务发现时二选一：

- 开发/测试（零外部依赖）：在装配声明里注入内存实现 `appkit.Registrar(inmemory.New())`（示例见 `_example/bald/internal/bootstrap/assemble.go`）——启动自动 Register、停机自动 Deregister；注册中心生效时启动日志会出现 `appkit registered`（含最终 endpoints）。
- 生产：yaml `registry` 段声明后端与连接参数（如 `type: "nacos"` + `nacos: { server_addrs: [...] }`），后端 Provider 由 contrib 包加法式提供（接入范本见 `_example/bald/internal/bootstrap/register_nacos.go`）。registry 段不支持热更新，变更需重启。

### 下一步

- [用 bald CLI 起步新服务](../用%20bald%20CLI%20起步新服务.md)：AppSpec 逐字段参考、三道门与 rerun 语义、gen domain / gen proto、授权接线、故障排查表。
- [产品介绍](../introduction/README.md)：设计理念。
- [Bald 配置系统](../Bald%20配置系统.md)：四源优先级链、env 命名规则、热更新粒度。
- [新增业务域 - Domain 与 gen domain](../新增业务域%20-%20Domain%20与%20gen%20domain.md)：把业务域挂进服务（域自持路由与 gRPC 注册，装配改动 0）。
- [Provider 接入 - contract.AppProvider 与段 Provider](../Provider%20接入%20-%20contract.AppProvider%20与段%20Provider.md)：接数据库等第三方后端（范本 `contrib/store-gorm`）。

