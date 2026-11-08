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

bald 是一个 **SPI + 适配器 + 声明式装配** 的 Go 多模块框架：契约层只定义接口（`transport.Server`、`broker.Broker`、`cache.Cache`、`registry.Registrar`/`Discovery`…），每种协议/后端各占一个独立 module 实现契约，`bconf` 用 protobuf 把「服务该怎么装配」固化成强类型配置，`bootstrap` 做分层合并，`pkg/appkit` 的 **v2 Provider** 把声明解析成执行计划（Plan）再逐个构建（Build），最终由 `appkit.Run` 统一启动与停机。

仓库有 **96 个 `go.mod`**（`find . -name go.mod`，含 `_example/bald`），派生出 **95 个发布/验证单元**（`scripts/list-modules.sh`，排除 `_example*`），无提交的 `go.work`；模块间靠每个子 module 的相对 `replace` 组成可编译闭包，CI 与本地 Taskfile 共用同一条模块派生（`scripts/list-modules.sh`，内部是 `find`）。唯一没有目录 `replace` 的同仓消费者是 `cli`——它按 tag 消费 `bconf`，这是它存在的理由（风险评审 §3.4）。本机联合开发可以临时建 `go.work`，但它已进 `.gitignore`，且 Taskfile 顶层与 CI 都把 `GOWORK` 置 `off`：`verify-modules` 开头有一条 `[ "$(go env GOWORK)" = "off" ]` 自检，一旦 workspace 生效就地让任务失败，避免逐模块判据从「发布形态」静默退化成「本机形态」。

## 2. 分层与模块清单

| 层 | 模块 | 角色 | 证据 |
|---|---|---|---|
| 装配 module | `github.com/kalandramo/bald/pkg` | **`pkg/` 全体的宿主 module**：appkit、store、audit、authn、authz、middleware、contextx、id、crudbridge、validation、testkit、metricshttp（原 `observability/`），加 13 条本地 `replace` | `pkg/go.mod:1` |
| 聚合根 | `github.com/kalandramo/bald` | 只剩文档与架构制品的机器闸门 `internal/docscheck`（`go list ./...` 实测 1 个包）、19 条本地 `replace`、**0 条 require**；**已无任何模块 require 它** | `go.mod:1`、`go list ./...` |
| 配置契约 | `bconf` | protobuf 生成的强类型配置契约（`bootstrapv1.BootstrapConfig`、`appspec/v1`） | `bconf/proto/`、`bconf/gen/`、Taskfile `proto-contract`（`Taskfile.yml:63-67`） |
| 配置源 | `bconfig` | 配置「从哪里读」：Reader/Watcher/Decoder + file/env/etcd/consul/nacos/vault/apollo | `bconfig/bconfig.go:3-16` |
| 装配桥 | `bootstrap` | 分层合并内核（viper 已退役）+ `ServerProvider` 工厂 + 远程配置驱动 | `bootstrap/config/config.go:1-17`、`bootstrap/server.go:25,89,137` |
| 组合层 | `pkg/appkit`（`pkg` module 内） | v2 Provider `Plan`/`Build`/`Segments`、`Run`/`stopAll` 生命周期、`std` 标准装配、`mech`/`appcli`/`Domain` | `pkg/appkit/provider.go:1-24`、`pkg/appkit/appkit.go:561-703` |
| 横切层 | `pkg/middleware`（统一契约 + 三桥 + `{bundle,gin,grpc}`） | 统一 `Handler`/`Middleware` 契约经三桥（GinHandler/Unary/StreamServerInterceptor）服务双协议，内置件一份实现（含 Observability）；gin/grpc 子包委托统一件，`bundle.Gin()`/`GRPCInterceptors()` 为 deprecated 兼容门面（方案 S） | `pkg/middleware/handler.go:13-16`、`pkg/middleware/bundle/bundle.go` |
| SPI | `transport` `broker` `cache` `metrics` `registry` `ratelimit` `circuitbreaker` `retry` `encoding` `log` `health` `berrors` | 只有接口与少量通用工具 | `transport/server.go:14-22`、`broker/broker.go:9-33`、`cache/cache.go:28-69`、`registry/registry.go:62-75` |
| 适配器 | `transport/*`(19) `broker/*`(4) `cache/*`(3) `registry/*`(4) `log/*`(5) `metrics/*`(3) `encoding/*`(12) `ratelimit/*`(3) `circuitbreaker/*`(4) `oss/{minio,s3}` `ai/*`(3) `workflow/argo` `cobramcp` | 每个后端一个 module | 各 `*/go.mod` |
| 接入子包 | `*/contract`（44 个目录，`find . -type d -name contract` 可复跑） | 把适配器注册成 appkit v2 Provider —— **这是适配器与装配层唯一的接缝** | `transport/gateway/contract/provider.go:32`、`registry/etcd/contract/provider.go:16`、`oss/minio/contract/provider.go:17` |
| 扩展 | `contrib/*`（8 个独立 module） | store-gorm/mongo、audit-*、authn-jwt、authz-casbin、config-nacos、observability-otlp；`contrib/database` 只是目录（无 `.go` 也无 `go.mod`） | `contrib/*/go.mod` |
| 代码生成 + 发布工具 | `github.com/kalandramo/bald/cli`（`cli/cmd/bald` + `cli/internal/{codegen,release}`） | `gen app/domain/proto/store` 从 `appspec.json` 产出 owned/seeded 文件树；`release plan/sync/changelog/tag` 驱动多模块发布。该 module 的 `go.mod` **零 `replace`**、按 tag 消费 `bconf`——这是它区别于仓库根的唯一目的（风险评审 §3.4） | `cli/go.mod`、`cli/internal/codegen/appspec_tmpl_*.go` |
| 样例 | `_example/bald` | 已迁入生成树的真实消费方 | `_example/bald/internal/bootstrap/assemble.gen.go:1-7,42-47` |

`transport/` 与 `transport/grpc` 的区别是理解本仓库的钥匙：前者是 **接口**（`Server`：`Start(ctx)/Stop(ctx)/Endpoint()`），后者是 **一个实现**。`transport/gateway` 是复合体——它内嵌 `*httpserver.HTTPServer` 再加 grpc→REST 反向代理（`transport/gateway/gateway_server.go:25`），`transport/mcp` 则直接包装 `mark3labs/mcp-go`，但同样实现了 `Start/Stop/Endpoint`（`transport/mcp/server.go:106,186,233`）。

<details>
<summary>完整 module 清单（95 个，与 <code>scripts/list-modules.sh</code> 同源）</summary>

下面这块是**机器校验的**：`internal/docscheck` 的 `TestModuleListMatchesDerivation`
把它逐行与 `scripts/list-modules.sh` 的派生结果比对（同一条谓词：仓库内含 `go.mod`
的目录，排除 `.git/` 与 `_example*`）。新增 module 没同步这里，`go test ./...` 就响。
根模块写作 `.`。

```module-list
.
ai/eino
ai/langchaingo
ai/openai
bconf
bconfig
berrors
bootstrap
broker
broker/kafka
broker/rabbitmq
broker/redis
broker/rocketmq
cache
cache/loadable
cache/local
cache/redis
circuitbreaker
circuitbreaker/hystrix
circuitbreaker/sentinel
circuitbreaker/sres
circuitbreaker/vegas
cli
cobramcp
cobramcp/examples/positional-args
cobramcp/examples/positional-args-mcp-server
cobramcp/examples/task
contrib/audit-gorm
contrib/audit-stream
contrib/authn-jwt
contrib/authz-casbin
contrib/config-nacos
contrib/database/gorm
contrib/database/mongodb
contrib/observability-otlp
contrib/store-gorm
contrib/store-mongo
encoding
encoding/avro
encoding/bson
encoding/cbor
encoding/flatbuffers
encoding/gob
encoding/json
encoding/msgpack
encoding/proto
encoding/thrift
encoding/toml
encoding/xml
encoding/yaml
health
log
log/aliyun
log/charm
log/loki
log/otelslog
log/sentry
log/tencent
metrics
metrics/datadog
metrics/otel
metrics/prometheus
oss/minio
oss/s3
pkg
ratelimit
ratelimit/bbr
ratelimit/sentinel
ratelimit/tokenbucket
registry
registry/consul
registry/etcd
registry/kubernetes
registry/nacos
retry
selector
selector/balancer
transport
transport/asynq
transport/client
transport/cron
transport/gateway
transport/graphql
transport/grpc
transport/http
transport/http3
transport/kafka
transport/mcp
transport/rabbitmq
transport/redis
transport/rocketmq
transport/sse
transport/subscribe
transport/tcp
transport/web
transport/webrtc
transport/websocket
workflow/argo
```

</details>

## 3. 装配主干（配置 → 计划 → 运行）

```
appspec.json / bconf 契约
   └─ cli/internal/codegen  ──生成──▶  internal/bootstrap/assemble.gen.go   (owned)
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

## 4. 拓扑事实：`pkg` module 是「枢纽」（单向扇入，无环）

**这是本架构最需要理解的一点。** 装配层与能力包现在自成一个 module `github.com/kalandramo/bald/pkg`，23 个适配器 module 把它当普通库引用；聚合根 `github.com/kalandramo/bald` 退成只装本仓工具的 module，实测已无任何模块 require 它。

```
pkg module 侧（require 13 个兄弟 module）
  bconf / berrors / bootstrap / health / log / ratelimit(+tokenbucket) /
  registry / transport / transport{/,/grpc,/http,/web}
  pkg/appkit/provider.go          ──▶ bootstrap 契约 + bconf/gen
  pkg/middleware/bundle           ──▶ transport{,grpc,http,web}（经 std 装配）

适配器 module 侧（单向指回 pkg，pkg 不认识它们）
  transport/gateway/go.mod            require github.com/kalandramo/bald/pkg
  transport/gateway/contract/provider.go  import github.com/kalandramo/bald/pkg/appkit
  registry/etcd/contract/provider.go      import github.com/kalandramo/bald/pkg/appkit
  oss/minio/contract/provider.go          import github.com/kalandramo/bald/pkg/appkit
```

**曾经存在的环**：`根 → bootstrap → transport/gateway → 根`（`bootstrap/server.go` 里硬编 gateway 分支 + `pkg/appkit` 的 `WithGatewayRegister`）。转码面整体交给 `transport/gateway/contract` 的 Provider 先断掉那条链；随后 `pkg/` 独立成 module，把「适配器 require 聚合根」的 22 条边整体改指 `pkg`。实测（同一份 Tarjan 脚本读各 `go.mod` 的 require 边；改前取 `git archive main` 的快照，改后取工作树）：

| 量 | 拆分前（main） | 拆分后 |
|---|---|---|
| `go.mod` 总数 / CI 派生 module 数 | 93 / 92 | 94 / 93 |
| 根 module 内的包（`go list ./...`） | 27 | 4 |
| require 根 module 的非示例模块 | 22 | **0** |
| require `bald/pkg` 的模块（含 `_example/bald`） | — | 23 |
| 根自己 require 的同仓 module | 13 | 1（`bconf`） |
| module require 边总数 | 311 | 312 |
| 大小 >1 的 SCC | 无（DAG） | 无（DAG） |
| 全仓本地 `replace` 条数（除 `_example`） | 410 | 423 |

CLI 拆分（风险评审 §3.4 走 B）在这张表之上又动了一次，2026-11-03 实测：`go.mod` 总数 94 → **95**、派生 module 93 → **94**（新增 `cli`）；根 module 内的包 4 → **1**（只剩 `internal/docscheck`）；根自己 require 的同仓 module 1（`bconf`）→ **0**（`bconf`/cobra/protobuf/yaml 四条 require 随 CLI 迁进 `cli/go.mod`）；全仓 `replace` **仍是 423**——根那 19 条一条没动，`cli` 新增的是 0 条。唯一变的是消费姿势：`cli` 是本仓第一个**没有目录 `replace` 兜底**的同仓消费者，它按 tag 拉 `bconf`。`cli/v0.1.0` 已于同日随根 `v0.37.1`、`pkg/v0.2.1` 一起发出（`d4396185`），零 `replace` 夹具里 `go install github.com/kalandramo/bald/cli/cmd/bald@v0.1.0` 实测装得上且生成的骨架能起进程——§3.4 那条缺陷到此闭环。

require `bald/pkg` 的模块（**33 个非示例模块** + `_example/bald`，可复跑判据：`for g in $(find . -name go.mod -not -path './.git/*' -not -path './_example*'); do grep -q 'kalandramo/bald/pkg ' "$g" && echo "$g"; done`）：
`transport/*`(14)（除 `http`/`grpc`/`web`/`subscribe` 之外的全部适配器——`{sse,asynq,gateway,cron,tcp,websocket,http3,graphql,webrtc,mcp,kafka,rabbitmq,redis,rocketmq}`，后九个是各 `*/contract` 工厂包把 `appkit` 带进来的）、`registry/*`(4)、`contrib/*`(8)、`metrics/*`(3，§7.27 的 instrumentation 三族)、`oss/{s3,minio}`、`cache/redis`、`broker/redis`。
**只有** `transport`（根 SPI 模块）与 `transport/{http,grpc,web,subscribe}` 不依赖 `pkg`，即只依赖 `transport`/`encoding`/`broker`/`berrors`/`log` 这些叶子契约模块。上面那张表是 2026-11-03 那次 pkg 拆分的一次性快照，「现」列不含此后各批 `*/contract` 工厂的 require 增长——要当前数取本节判据，不要读那张表。

扇入的代价换了对象，没有消掉：

- `pkg` 的 API 变更对 23 个消费者在**编辑期不可见**，只在打 tag 时炸；发版顺序仍是人工约束（对应提交 `ca981b1` "tag 分批推送，避免触发流水线漏跑"、`ec4223f` "归位 bconf 的消费者 require"）。
- 真正消掉的是另一条：`cmd/bald` 与 `internal/{codegen,release}` 只有本仓工具在用，它们改动不再产出适配器必须追的 tag；根也不再靠 13 条兄弟 require 撑着。§3.4 走 B 之后这两包迁到 `cli` module，根连 `bconf` 这条 require 也不留了——根现在只剩 `internal/docscheck`，0 条 require。
- **不降反升的一条**：全仓 `replace` 从 410 涨到 423——`pkg/go.mod` 自己新增 13 条，适配器侧是 1:1 平移。评审当时把「`replace` 数量下降」记作收益，实测不成立，见 `risk-review.md` §3.1 收口。
- 下游若在同一个 build 里既 require 根 `v0.34.1`（最后一个含 `pkg/*` 的 tag）又 require `bald/pkg`，会 `ambiguous import`。迁移动作是**替换**而非新增依赖。
- `go.mod` 与 `bootstrap/go.mod` 仍保留指向 `transport/gateway` 的本地 `replace`（已无对应 require，属遗留；保留是为了将来复用不必重新接线）。

严重度评估见 `risk-review.md` §3.1。

## 5. SPI 契约清单

| SPI | 位置 | 方法 | 实现数 |
|---|---|---|---|
| `transport.Server` | `transport/server.go:14-22` | `Start(ctx)` `Stop(ctx)` `Endpoint()` | `transport/*` 的 19 个模块（`mcp` 同样实现：`transport/mcp/server.go:106,186,233`） |
| `transport.Endpoint` 解析 | `transport/endpoint.go:26` `Extract` | 支持 `:0` 动态端口 | 由 `appkit.waitForEndpoints` 消费（`appkit.go:816-848`） |
| `broker.Broker` | `broker/broker.go:9-33` | `Name/Options/Address/Init/Connect/Disconnect/Publish/Subscribe` + 泛型 `Subscribe[T]`(:36) | kafka/rabbitmq/redis/rocketmq |
| `cache.Cache` | `cache/cache.go:28-69` | `Get/Set/SetNX/GetMulti/Delete…` | local/redis（loadable 是**读穿组合器**，不是后端） |
| `registry.Registrar` + `registry.Discovery` | `registry/registry.go:62`、`:70` | `Registrar`: `Register`/`Deregister`；`Discovery`: `GetService`/`Watch`（返回 `Watcher`，:78）。根包**没有一个叫 `Registry` 的类型**（后端实现侧有：`registry/{etcd,consul,nacos,kubernetes}` 各自的 `Registry` 结构体）——两面分开，注册端不必实现发现 | etcd/consul/nacos/kubernetes |
| `bconfig.Reader/Watcher/Decoder` | `bconfig/bconfig.go:3-16` | 配置源抽象 | file/env/etcd/consul/nacos/vault/apollo |
| `metrics.Metrics` | `metrics/metrics.go:15-19` | `Counter`/`Histogram` | otel/prometheus/datadog |
| `encoding.Codec` | `encoding/` + 12 个格式 module | 编解码注册表，`broker.Marshal` 缺 codec 时 fail-fast（`broker/encoding.go:34`） | 12 |
| `appkit.Provider` (v2) | `pkg/appkit/provider.go:1-24` | `Option`+`Segments`+`Plan`+`Build` | `std` + 43 个 `*/contract` + 业务自定义 |

## 6. 未接线面（reference-only：存在但仓内无人调用）

置信度 **high**。口径：**这些按 reference-only 保留**——不删除、不降级为示例、
不假装已接线；文档与 README 明确标未接线，改动它们时以「仓外消费方可能存在」为前提。
接了线的项从本清单移出，但在末尾留一行去向（清单必须与上面那条 grep 判据一致，否则宁可删条目也不留过期状态）。
判据可复现（属主包之外的每个包都算一次 importer）：

```bash
find . -name '*.go' ! -name '*_test.go' -not -path './.git/*' -not -path './<pkg>/*' \
  | xargs grep -l 'kalandramo/bald/<pkg>' | grep -v '^./<pkg>/'
```

- `circuitbreaker/{vegas,sres,sentinel}` —— 契约接口 `circuitbreaker` 与其 `hystrix` 实现**已接线**（见本节末），这三个后端仍只被自己的测试驱动，换算法属代码级注入（`bundle.CircuitBreaker` 接受任意 `circuitbreaker.CircuitBreaker`）。判据同上，按 `circuitbreaker/<后端>` 逐个 grep。
- `workflow/argo`、`ai/{openai,eino,langchaingo}`、`oss/{s3,minio}` —— 有 `*/contract` Provider，仓内零消费者、零示例。
- 四个 broker 型 transport 后端（`transport/{kafka,rabbitmq,redis,rocketmq}`）**仍是「仓内零消费者」，但缺席的原因已换**：装配面（服务器面的 `contract` 工厂）已随 §7.28 方向②第三批补齐（2026-11-06，见本节末去向条），所以业务侧现在「一行 `AppProvider` + 一段 YAML」就能起这台服务器，剩下的只是本仓没有这样的业务进程。`transport/{graphql,webrtc,mcp}`、`transport/{tcp,websocket,http3}` 同在此列（第二、第一批）。
- 中间件依赖未注入即整层省略：`bundle.New()` 不给 `Authn` 时链中静默无认证层（风险 §6.1）。现已要求显式表态——`bundle.Public()` 声明有意公开，否则 `Bundle.Verify()` 报错、出链时告警（`pkg/middleware/bundle/bundle.go` 的 `Verify`）；生成脚手架默认写出 `bundle.Public()` 并留 TODO。
- 中间件链上仍未接线的只有 **stream 面的形状**：`GRPCStreamInterceptors()` 出的链是 `Error → RateLimit`——超时按口径不默认挂（unary 的 `default_timeout_ms` 套在流上会静默变成「流的最大存活时长」），认证/授权/审计只有 unary 形态。`server.grpc.middleware` 的 `recovery`/`logging`/`tracing`/`validate` 四段是空 message，`FromGRPCMiddleware` **不翻译**它们（写了不改变任何行为，别当配置面）。契约侧也没有 `authn` 段：认证姿态是业务代码里的一行显式声明（`bundle.Public()` 或 `bundle.Authn(...)`），见 §6.1 与《Bald 中间件设计》。

**已接线的原未接线项**（去向留在此处，避免与上面的判据脱节）：

- 契约 middleware 段的**读取方**（风险 §6.2③）——先分清两轮工作：限流/超时本身（unary `GRPCInterceptors()`、stream `GRPCStreamInterceptors()`、`server.grpc.middleware` 两段与 `bundle.FromGRPCMiddleware`）在前两轮已补齐；本轮补的是「谁在启动时读那两个段」。此前它们是「有桥没读者」——两座桥翻译得对，但框架里没有任何组件调用它们，业务必须自己显式走桥。现在有固定消费口：`appkit.WithHTTPContract(fn)` / `appkit.WithGRPCOptionsContract(fn)` 在契约装载后、变体与域接线之前求值一次，把对应段的指针直接交给业务钩子（`pkg/appkit/contract_faces.go`）；钩子给出的 `close` 进停机 Effect，启动失败也回滚释放。codegen 两面脚手架据此对称改形（seeded 的 `apiserver.NewRouter(mw)` + `bootstrap.newGRPCChain(mw)`，内部就是两座桥），角色的 `configs/<app>-<role>.yaml` 经 `inheritMiddleware` 继承两段。判据（`grep -rln 'WithHTTPContract\|WithGRPCOptionsContract' --include='*.go' . | grep -v _test.go`）落在三处：`pkg/appkit` 7 个文件（`bootstrap.go` 定义类型与 Option、`contract_faces.go` 求值、`decl.go`/`variant.go`/`domain.go`/`plan_compile.go` 的判据与计划投影、`appkit.go` 的 `HTTPHandler()` 文档口径）、`cli/internal/codegen` 3 个文件（两面脚手架模板 + `app.go` 的角色契约继承）、`_example/bald` 4 个文件（owned 的 `assemble.gen.go` + seeded 的 `apiserver.go`/`custom.go`/`grpc.go`）。仓内实证：`_example/bald/internal/bootstrap/middleware_e2e_test.go` 起真进程从 YAML 断到 429 / `RESOURCE_EXHAUSTED` / 提前 503 / `DEADLINE_EXCEEDED` / 熔断开窗后的 503 与 `UNAVAILABLE`+`CIRCUIT_OPEN`。
- `circuitbreaker/` 契约与 `circuitbreaker/hystrix`（风险 §6.3 的延伸）—— 服务端入口的自我防护面有了消费方：`pkg/middleware/gin/circuitbreaker.go`（`CircuitBreakerMiddleware`/`NewCircuitBreaker`，默认拒绝码 503）与 `pkg/middleware/grpc/circuitbreaker.go`（`CircuitBreakerInterceptor`，拒绝码固定 `UNAVAILABLE` + `Reason=CIRCUIT_OPEN`，失败口径 `IsFailureStatus`），由 `bundle.CircuitBreaker`/`GRPCCircuitBreaker` 挂进两面链的「授权之后、限流之内、超时之外」。契约段 `server.{http,grpc}.middleware.circuit_breaker` 经 `bundle.CircuitBreakerFromMiddleware`/`GRPCCircuitBreakerFromMiddleware` 翻译，缺段即不挂这一层（opt-in）。判据：`find . -name '*.go' ! -name '*_test.go' -not -path './circuitbreaker/*' | xargs grep -l 'kalandramo/bald/circuitbreaker'` 命中恰为上面两个中间件文件；仓内实证为 `_example` 的 4 条熔断端到端（含「缺席不触发」与 env 覆盖各一条）。
- `registry.Discovery` —— 原「会注册、不会解析」的生产侧半环已闭合：`transport/gateway` 的 resolver 消费 `GetService`+`Watch`（`transport/gateway/discovery.go:63,64`），解析出的端点先归一化（`grpc://` 剥前缀、其它 scheme 的面丢弃，`grpcAddressOf`——bald 自己注册的端点就带 scheme）。`server.gateway.backend_grpc_addr` 填 `discovery:///<服务名>` 即由注册中心解析，来源由装配作用域自动派生（`discoveryFromScope`，`transport/gateway/contract/discovery.go`；读取口是本次新增的 `App.Registrar()`，`pkg/appkit/appkit.go:342`），消费方**只改配置、不改代码**。两条容易踩的实现语义已写进代码注释：① 服务名要按后端真实命名写，nacos 把一个实例按端点拆成 `<名>.<scheme>`，那里得写 `discovery:///<服务名>.grpc`；② 首次快照失败（如 nacos 对未注册服务直接报 `instance list is empty!`）**不**返回给 gRPC，只 `ReportError` 保订阅——resolver 重建只在重连退避到期才发生，返回错误等于放弃恢复。仓内消费者：`discovery_e2e_test.go`（真应用 + inmemory，跑 REST→转码→gRPC）、`discovery_registry_e2e_test.go`（etcd、nacos 各一轮真机，opt-in：`-short` 跳过、服务不可达即 Skip）。仍未覆盖的：`transport/http` 与业务自拨的 gRPC 客户端没有 resolver；`gatewaycontract.HTTPProvider` 按设计**故意不**派生来源（它的上游是本进程 gRPC 面自己的绑定地址，填 `discovery:///` 是配置错误）；consul、kubernetes 两个后端各有真机 CI 腿（`integration` / `integration-kubernetes` 作业，见登记册 §7.1 两条补条），但**仓内没有消费方**——它们的注册/发现只被自己的测试驱动，不经过 gateway resolver。判据：`grep -rn '\.GetService(' --include='*.go' . | grep -v _test.go` 的非 registry 命中只有 `transport/gateway/discovery.go`。
- `retry/`（**半**：原语已接，循环未接）—— 退避曲线与抖动只留一份实现：`broker/kafka/subscriber.go` 的订阅退避用 `retry.ExponentialBackoff` + `retry.EqualJitter`，`registry/etcd/registry.go` 的重注册退避用 `retry.ExponentialBackoff` + `retry.FullJitter` 并截到 `defaultRetreatMax`，曲线与抖动能被契约段改写（见下条）。判据：`grep -rl '"github.com/kalandramo/bald/retry"' --include='*.go' .` 命中 5 个文件——`broker/kafka/subscriber.go`、`registry/etcd/{options,registry}.go`、`registry/etcd/contract/contract.go`、`registry/etcd/retreat_policy_test.go`（`retry` 自身不 import 自己；`ratelimit/bbr/bbr.go` 的那处提及是注释，不在命中里）。`retry.Retrier` 的循环仍是 reference-only，两处刻意不接：① 服务入口链——服务端自动重试会重复执行有副作用的 handler；② `bconfig/apollo` 的空文档等待——它的谓词是内容而非 error、「重试耗尽仍为空」是合法结果、payload 要靠闭包带出，三条都撞 `Retrier.Do` 的签名，理由见风险评审 §2「retry 半」。
- 10 个已实现适配器面的**服务器契约工厂**（风险 §7.28 方向②，三批全落，2026-11-06）—— 缺口原先是「能力在、装配面缺席」：`transport/{tcp,websocket,http3}`（第一批）、`transport/{graphql,webrtc,mcp}`（第二批）、`transport/{kafka,rabbitmq,redis,rocketmq}`（第三批）各自新增 `*/contract` 包，把 `bootstrap.ServerProvider` 那一层（`bootstrap/server.go:25`，早已在 `transport/{sse,cron,asynq,gateway}` 各实现过一次）按同一模板复用，`server.*` 对应子段本来就是契约里的字段。至此 `server` 段 32 个子段里有工厂的 **16 个**（`transport/*` 侧 14 个 `contract` 包 + `http`/`grpc` 由 bootstrap 内置），**「已实现适配器的服务器面在契约外」归零**——剩余的缺口只在方向①（段声明了但仓里没实现）与方向③（客户端/出站面没有契约表达），都属 owner 口径决策，见 §7.28。判据：`find transport -type d -name contract | wc -l` = 14；取用姿势与三段口径（含可抄的 `MustServe` + YAML）在《Provider 接入 - contract.AppProvider 与段 Provider》§2.1。第三批的形状差异（也是它不能用前两批模板直接抄的原因）：broker 型服务器的 `Start` 消费订阅/消费者注册表，所以**工厂不代劳订阅**——订阅经 `WithAfterBuild` 由业务侧注册，`Endpoint()` 在未 `Start` 时恒空是就绪判据，而 codec 行的读者是 `broker` 注册表，未注册即构造期报错。
- 契约层的**退避曲线**（`bootstrap.v1.Backoff`）—— 「几次」之外终于能写「每次等多久」：`bconf/proto/bootstrap/v1/common.proto` 新增顶层消息 `Backoff`（`initial`/`factor`/`step`/`max` + `curve`{EXPONENTIAL,LINEAR,FIXED} + `jitter`{NONE,FULL,EQUAL}），第一个表面是 `Registry.Etcd.backoff`（字段号 7，`optional`），由 `registry/etcd/contract` 的 `retreat()` 翻成 `retry.Backoff` + `retry.Jitter`，再经 `etcd.WithRetreat` 覆盖默认策略。缺省分两层：段缺席＝整条曲线沿用后端默认（`retreat(nil)` 返回两个 nil）；段存在＝`initial` 必填且 `>0`、除 `FIXED` 外 `max` 必填且 `>0`，`factor` 未写由 `retry` 按 2 归一、`jitter` 未写落回 nil 即**只换曲线不换抖动**。**刻意不做逐字段继承后端默认**——那要求契约层持有每个后端的默认数字，同一数字住两处必漂。零原点（忙轮询）与无上界指数（高位次乘出负 duration）不可表达，判错并点名键。`broker/kafka` 这轮**没挂**：它的旋钮 `WithReadBackoffMin/Max` 实测零调用者、`broker/kafka/contract.Provider` 也不被仓内代码调用，挂上去就是在 Provider 层造「写了没人读」的字段。共享翻译包还没抽（只有 1 个消费者），第二个表面接入时升格，边方向定为 `retry ← 桥`、不给 `bconf` 加依赖，理由与升格条件见《Bald 重试设计》「契约曲线」。随批修掉一处**既有产品缺陷**（不限新字段）：`bconf` 的 `coerceDuration` 字符串分支原先只校验不重排，`1500ms`/`5m` 这类合法 Go 写法会被 protojson 拒掉，所有走契约的 `Duration` 字段都在这条路径上。判据三层：`bconf` 3 条解码（含四种 duration 写法）、`registry/etcd/contract` 7 条桥接（含坏段 fail-fast）、`registry/etcd` 6 条策略与曲线；变异验活 8 条各自判红，1 条**存活**（`Provider` 里 `opts = append(opts, etcd.WithRetreat(b, j))` 那行——删掉全绿，与契约映射的其它每一行同样只有需真 etcd 的集成腿能观测，本轮未接，见登记册）。

## 7. 阅读顺序建议

1. 先读 §1 与 §5 建立「契约 vs 适配器」的直觉；
2. 想加一个新后端 → 读 §2 的 `*/contract` 说明 + `pkg/appkit/provider.go`；
3. 想改配置语义 → 读 `bconf/proto` → `bootstrap/config/config.go` → `pkg/appkit` 的 Resolve；
4. 想知道请求怎么流 → `flows.md`；
5. 想动 `pkg` module 的 API → 先读 §4 与 `risk-review.md` §3.1。

## 8. 假设与缺口

| 项 | 置信度 | 说明 |
|---|---|---|
| 「服务间调用不使用 Discovery，走配置地址直连」 | **已被推翻**（2026-11-02） | 评审当时的依据：gateway 用契约 `Grpc.addr` 拨号（`transport/gateway/gateway_server.go:170`，地址由 `transport/gateway/contract/contract.go` 从契约 `server.gateway.backend_grpc_addr` 传入，缺省回退 `server.grpc.addr`）。现在该地址可写 `discovery:///<服务名>` 并经装配作用域解析（§6 末「已接线的原未接线面」）；直连仍是默认。外部消费方（bald-admin）实际采用面未在本文核验 |
| 「95 个 `go.mod` 的真实可运行面远小于发布面」 | high | 见 §6 与 `risk-review.md` §2 |
| Windows 周界 | unknown | `.github/workflows/windows.yml` 每周一/手动 dispatch；该面本轮未验证 |
| 运行时拓扑（进程实际部署形态、副本、网络边界） | unknown | 仓内无 IaC/清单，本次未做 `deployment-topology-analyzer` |
