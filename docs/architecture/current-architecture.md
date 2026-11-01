# bald 当前架构（现状视图）

> 产出方式：`system-modeler` + `c4model`（结构视图）、`graphviz`（依赖图）。
> 证据日期：2026-11-01，`main` @ `ca981b1`，工作树干净。
> 读者：需要在 bald 上做改动的人（框架接手者、消费方服务作者）。
> 范围：**当前状态**，不含目标态。所有节点/边可回溯到仓库内文件；未证实的推断单独标注置信度。
> 核对方式：文中 `路径:行号` 由脚本抽出后逐条打开源文件比对；`.dot` 与 `.dsl` 均已真实渲染/解析校验（graphviz 16.1.0 + 官方 `structurizr-dsl` 6.2.4 解析器，命令见 `README.md`）。

配套文件：
- `bald-c4.dsl` — C4 结构源（landscape / 服务视角 / 模块视角），供 Structurizr/DSL 预览打开
- `module-deps.dot` — 模块依赖分层图（含跨层回边高亮），供 DOT 预览打开
- `flows.dot` + `flows.md` — 调用/消息/生命周期流
- `risk-review.md` — 风险登记册

---

## 1. 一句话架构

bald 是一个 **SPI + 适配器 + 声明式装配** 的 Go 多模块框架：契约层只定义接口（`transport.Server`、`broker.Broker`、`cache.Cache`、`registry.Registry`…），每种协议/后端各占一个独立 module 实现契约，`bconf` 用 protobuf 把「服务该怎么装配」固化成强类型配置，`bootstrap` 做分层合并，`pkg/appkit` 的 **v2 Provider** 把声明解析成执行计划（Plan）再逐个构建（Build），最终由 `appkit.Run` 统一启动与停机。

仓库有 **93 个 `go.mod`**（`find . -name go.mod`），无 `go.work`；模块间靠每个子 module 的相对 `replace` 组成可编译闭包，CI 用 `find` 自动发现全部模块（`.github/workflows/ci.yml:61`）。

## 2. 分层与模块清单

| 层 | 模块 | 角色 | 证据 |
|---|---|---|---|
| 聚合根 | `github.com/kalandramo/bald` | 同时是 **`pkg/` 库**（appkit、store、audit、authn、authz、middleware、contextx、id、crudbridge、validation、testkit）、`cmd/bald`、`internal/{codegen,docscheck,release}`、`observability/` 的宿主，以及 19 条本地 `replace` 的聚合点 | `go.mod:1,5-37,228,230` |
| 配置契约 | `bconf` | protobuf 生成的强类型配置契约（`bootstrapv1.BootstrapConfig`、`appspec/v1`） | `bconf/proto/`、`bconf/gen/`、Taskfile `proto-contract`（`Taskfile.yml:63-67`） |
| 配置源 | `bconfig` | 配置「从哪里读」：Reader/Watcher/Decoder + file/env/etcd/consul/nacos/vault/apollo | `bconfig/bconfig.go:3-16` |
| 装配桥 | `bootstrap` | 分层合并内核（viper 已退役）+ `ServerProvider` 工厂 + 远程配置驱动 | `bootstrap/config/config.go:1-17`、`bootstrap/server.go:25,89,137` |
| 组合层 | `pkg/appkit`（根 module 内） | v2 Provider `Plan`/`Build`/`Segments`、`Run`/`stopAll` 生命周期、`std` 标准装配、`mech`/`appcli`/`Domain` | `pkg/appkit/provider.go:1-24`、`pkg/appkit/appkit.go:561-703` |
| 横切层 | `pkg/middleware/{bundle,gin,grpc}` | 链序固化的 gin 中间件链与 gRPC 拦截器链 | `pkg/middleware/bundle/bundle.go:171-211,224-241` |
| SPI | `transport` `broker` `cache` `metrics` `registry` `ratelimit` `circuitbreaker` `retry` `encoding` `log` `health` `berrors` | 只有接口与少量通用工具 | `transport/server.go:14-22`、`broker/broker.go:9-33`、`cache/cache.go:28-69`、`registry/registry.go:62-75` |
| 适配器 | `transport/*`(18) `broker/*`(4) `cache/*`(3) `registry/*`(4) `log/*`(5) `metrics/*`(3) `encoding/*`(12) `ratelimit/*`(3) `circuitbreaker/*`(4) `oss/{minio,s3}` `ai/*`(3) `workflow/argo` `cobramcp` | 每个后端一个 module | 各 `*/go.mod` |
| 接入子包 | `*/contract`（30 个目录） | 把适配器注册成 appkit v2 Provider —— **这是适配器与装配层唯一的接缝** | `transport/gateway/contract/provider.go:24`、`registry/etcd/contract/provider.go:16`、`oss/minio/contract/provider.go:17` |
| 扩展 | `contrib/*`（8 个独立 module） | store-gorm/mongo、audit-*、authn-jwt、authz-casbin、config-nacos、observability-otlp；`contrib/database` **无独立 `go.mod`**，属根 module | `contrib/*/go.mod` |
| 代码生成 | `cmd/bald` + `internal/codegen` | `gen app/domain/proto/store`，从 `appspec.json` 产出 owned/seeded 文件树 | `internal/codegen/appspec_tmpl_*.go` |
| 样例 | `_example/bald` | 已迁入生成树的真实消费方 | `_example/bald/internal/bootstrap/assemble.gen.go:1-7,42-47` |

`transport/` 与 `transport/grpc` 的区别是理解本仓库的钥匙：前者是 **接口**（`Server`：`Start(ctx)/Stop(ctx)/Endpoint()`），后者是 **一个实现**。`transport/gateway` 是复合体——它内嵌 `*httpserver.HTTPServer` 再加 grpc→REST 反向代理（`transport/gateway/gateway_server.go:20-24`），`transport/mcp` 则直接包装 `mark3labs/mcp-go`，但同样实现了 `Start/Stop/Endpoint`（`transport/mcp/server.go:106,186,233`）。

## 3. 装配主干（配置 → 计划 → 运行）

```
appspec.json / bconf 契约
   └─ internal/codegen  ──生成──▶  internal/bootstrap/assemble.gen.go   (owned)
                                        │  声明 appkit.Option / Domain / VariantImpl
                                        ▼
              bconf (强类型 BootstrapConfig)  +  bconfig (远程配置源)
                                        ▼
              bootstrap/config  分层合并：flag > env > file > contract source > remote bridge
                                        ▼
              pkg/appkit  v2 Provider
                   Plan(ctx, PlanScope)   → []Node（Kahn 拓扑序 + 段覆盖闸门）
                   Build(ctx, ApplyScope, Node) → (any, Cleanup, error)
                                        ▼
              appkit.Run  → errgroup 并发 Start 所有 transport.Server
                          → waitForEndpoints（`:0` 解析）→ register 到发现服务
                          → signal → deregister → stopAll（五阶段，逐阶段超时）
```

关键接缝：
- `bootstrap.ServerProvider func(ctx, *bootstrapv1.Server) (transport.Server, func(), error)`（`bootstrap/server.go:25`），配 `GrpcServerProvider`/`HttpServerProvider`（:89,:137）。
- `appkit.Provider` 接口（`pkg/appkit/provider.go:1-24`）：`Option` + `Segments()` + `Plan()` + `Build()`。自 v0.31.0 起 **只有 v2**，`AdaptLegacy`/`LegacyProvider` 等 v1 兼容面已删除。
- 变体（Q7）：`AppSpec.Variant.when` 用 `go/build/constraint` 表达式，命中后 effects 并入真实装配；`variantImpls`/`WithVariantImpl` 与声明在 plan 期对账（I12）。
- 自定义接线（Q9）：`customOptions(role)` / `customVariantOpts(name)` / `customSubcommands()` 三个 seeded 钩子承载声明表达不了的接线，`assemble.gen.go` 把 `customOptions` 放最后 → last-wins。

## 4. 拓扑事实：根 module 是「枢纽」（单向扇入，已不成环）

**这是本架构最需要理解的一点。** 根 module 既聚合又提供 `pkg/`，而 23 个适配器 module 把它当普通库引用：

```
根 module 侧（只 require 叶子契约与装配模块）
  go.mod:43-54  bconf / berrors / bootstrap / health / log / ratelimit(+tokenbucket) /
                registry / transport / transport{/,/grpc,/http,/web}
  bootstrap/server.go:13-15  ──▶ transport{,grpc,http}
  bootstrap/health.go:13     ──▶ transport/grpc
  pkg/middleware/gin/authn.go:15 ─▶ transport/web

适配器 module 侧（单向指回根，根不认识它们）
  transport/gateway/go.mod:28            require github.com/kalandramo/bald v0.34.1
  transport/gateway/contract/provider.go:24  import github.com/kalandramo/bald/pkg/appkit
  registry/etcd/contract/provider.go:16      import github.com/kalandramo/bald/pkg/appkit
  oss/minio/contract/provider.go:17          import github.com/kalandramo/bald/pkg/appkit
```

**曾经存在的环**：`根 → bootstrap → transport/gateway → 根`（`bootstrap/server.go` 里硬编 gateway 分支 + `pkg/appkit` 的 `WithGatewayRegister`）。转码面已整体交给 `transport/gateway/contract` 的 Provider，根与 `bootstrap` 不再 require `transport/gateway`，上述链已断。实测（`go list -e` 逐模块取 import，按最长前缀归到属主 module）：**89 个模块的内部依赖图是 DAG，含测试导入也无环**。

直接 require 根 module 的模块（**22 个非示例模块**，实测 `grep -E "^\s+github.com/kalandramo/bald v"`；另有 `_example/bald`，合计 23）：
`broker/redis`、`transport/{sse,asynq,tcp,webrtc,gateway,cron}`、`cache/redis`、`registry/{nacos,consul,etcd,kubernetes}`、`oss/{s3,minio}`、`contrib/{config-nacos,store-mongo,store-gorm,audit-stream,audit-gorm,authn-jwt,authz-casbin,observability-otlp}`、`_example/bald`。
其余 transport（`websocket`、`web`、`kafka`、`rabbitmq`、`redis`、`rocketmq`、`subscribe`、`mcp`）**不依赖根 module**，只依赖 `transport`/`encoding`/`broker`/`berrors`/`log` 这些叶子契约模块。

反向要求根 module 的模块与根自己 require 的模块**交集为空**，所以环不会回来；代价只是枢纽型依赖：
- 根 API 变更对 23 个消费者在**编辑期不可见**，只在打 tag 时炸；
- 发版顺序成为人工约束（对应近期提交 `ca981b1` "tag 分批推送，避免触发流水线漏跑"、`ec4223f` "归位 bconf 的消费者 require"）。
- `go.mod:5-37` 与 `bootstrap/go.mod` 仍保留指向 `transport/gateway` 的本地 `replace`（已无对应 require，属遗留；保留是为了将来复用不必重新接线）。

严重度评估见 `risk-review.md` §3.1。

## 5. SPI 契约清单

| SPI | 位置 | 方法 | 实现数 |
|---|---|---|---|
| `transport.Server` | `transport/server.go:14-22` | `Start(ctx)` `Stop(ctx)` `Endpoint()` | `transport/*` 的 18 个模块（`mcp` 同样实现：`transport/mcp/server.go:106,186,233`） |
| `transport.Endpoint` 解析 | `transport/endpoint.go:26` `Extract` | 支持 `:0` 动态端口 | 由 `appkit.waitForEndpoints` 消费（`appkit.go:816-848`） |
| `broker.Broker` | `broker/broker.go:9-33` | `Name/Options/Address/Init/Connect/Disconnect/Publish/Subscribe` + 泛型 `Subscribe[T]`(:36) | kafka/rabbitmq/redis/rocketmq |
| `cache.Cache` | `cache/cache.go:28-69` | `Get/Set/SetNX/GetMulti/Delete…` | local/redis（loadable 是**读穿组合器**，不是后端） |
| `registry.Registry` | `registry/registry.go:62-75` | `Registrar`(:62-67) + `Discovery`(:70-75，含 `GetService`/`Watch`) | etcd/consul/nacos/kubernetes |
| `bconfig.Reader/Watcher/Decoder` | `bconfig/bconfig.go:3-16` | 配置源抽象 | file/env/etcd/consul/nacos/vault/apollo |
| `metrics.Metrics` | `metrics/metrics.go:15-19` | `Counter`/`Histogram` | otel/prometheus/datadog |
| `encoding.Codec` | `encoding/` + 12 个格式 module | 编解码注册表，`broker.Marshal` 缺 codec 时 fail-fast（`broker/encoding.go:34`） | 12 |
| `appkit.Provider` (v2) | `pkg/appkit/provider.go:1-24` | `Option`+`Segments`+`Plan`+`Build` | `std` + 30 个 `*/contract` + 业务自定义 |

## 6. 未接线面（存在但无人调用）

置信度 **high**（全仓 grep 复核，非推断）：

- `retry/` —— 全仓唯一提及是文档注释（`ratelimit/bbr/bbr.go:68`），**零 importer**。
- `circuitbreaker/` —— 只被自己的 4 个后端 import（`circuitbreaker/{hystrix,vegas,sres,sentinel}/*.go`），**无 transport / middleware / appkit 消费方**。
- `registry.Discovery` —— `GetService` 的调用点只出现在 registry 后端自身与其测试；`appkit` 只用 `Registrar`/Deregister（`pkg/appkit/appkit.go:643,659,690`）。**服务发现目前是生产侧半环：会注册，不会解析。**
- gRPC 链上没有 `RateLimit` 与 `Timeout` 层（对比 `pkg/middleware/bundle/bundle.go:199-209` 的 gin 链，`:224-241` 的 gRPC 链到 Authz 就结束）。
- 中间件依赖未注入即整层省略：`bundle.New()` 不给 `Authn` 时链中静默无认证层（风险 §6.1）。现已要求显式表态——`bundle.Public()` 声明有意公开，否则 `Bundle.Verify()` 报错、出链时告警（`pkg/middleware/bundle/bundle.go:185-212`）；生成脚手架默认写出 `bundle.Public()` 并留 TODO。

## 7. 阅读顺序建议

1. 先读 §1 与 §5 建立「契约 vs 适配器」的直觉；
2. 想加一个新后端 → 读 §2 的 `*/contract` 说明 + `pkg/appkit/provider.go`；
3. 想改配置语义 → 读 `bconf/proto` → `bootstrap/config/config.go` → `pkg/appkit` 的 Resolve；
4. 想知道请求怎么流 → `flows.md`；
5. 想动根 module 的 `pkg/` API → 先读 §4 与 `risk-review.md` §3.1。

## 8. 假设与缺口

| 项 | 置信度 | 说明 |
|---|---|---|
| 「服务间调用不使用 Discovery，走配置地址直连」 | medium | 依据：gateway 用契约 `Grpc.addr` 拨号（`transport/gateway/gateway_server.go:93`，地址由 `transport/gateway/contract/provider.go:167` 从契约 `server.grpc` 段传入）；仓内无 resolver。外部消费方（bald-admin）是否有自研解析未在本次范围内核验 |
| 「93 个模块的真实可运行面远小于发布面」 | high | 见 §6 与 `risk-review.md` §2 |
| Windows 周界 | unknown | `.github/workflows/windows.yml` 每周一/手动 dispatch；该面本轮未验证 |
| 运行时拓扑（进程实际部署形态、副本、网络边界） | unknown | 仓内无 IaC/清单，本次未做 `deployment-topology-analyzer` |
