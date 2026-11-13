# Provider 接入 - contract.AppProvider 与段 Provider

面向要把一个能力（自研或第三方）接进 bald 装配层的人：标准能力怎么一行接入、整段自持的 Provider 怎么写、段覆盖与停机回收的语义、报错了怎么修。设计论证（装配引擎为什么用段驱动 Provider 而不是手工装配流）见 [Bald 声明式装配设计](../../devel/zh-CN/Bald%20声明式装配设计.md)，本文只讲操作。主线一句话：**能力进装配层的唯一形态是段驱动 Provider——声明消费哪个契约段，`Plan` 纯函数产出节点（阶段/优先级/依赖/停机约束），引擎按计划序调 `Build`，产物按段路径落统一资源面，cleanup 进停机 Effect 账本。** Provider 只有 v2 一种形态，没有 v1 适配器；第三方 Provider 直接实现 `Plan`/`Build`。

## 心智模型：一句话版

**Provider 即 Option：`Segments()` 声明契约段路径（顶层段名或子段路径），`Plan` 纯函数产出计划节点，`Build` 构建段产物，`Apply` 把自身追加进声明——Provider 值直接作为 `MustServe` 变参项。段缺失 = 跳过（合法）；同段精确路径末者胜；契约里出现而无消费者的顶层段 = 启动期报错（L3，fail-closed）。**

## 1. Provider 接口：三方法 + 一个嵌入

`pkg/appkit/provider.go`：

```go
type Provider interface {
    Option                        // Apply 把自身追加到声明——Provider 值可直接作为变参项
    Segments() []string           // 声明消费的契约段路径（非空；空路径装配期报错）
    Plan(ctx context.Context, s PlanScope) ([]Node, error) // 纯函数：阶段/优先级/依赖/停机约束
    Build(ctx context.Context, s ApplyScope, n Node) (any, Cleanup, error)
}
```

- **Segments：段路径即契约段**。顶层段名（"database"、"audit"）或子段路径（"database.sql"、"server.http"），精确匹配；段存在性由 protobuf 反射逐级判定（消息型字段非 nil 即 present）。契约共 17 个顶层段。
- **Option 同款 Apply**：Provider 嵌入 Option——与 `std.Standard()`、业务域声明值并列传入 MustServe（[新增业务域](./新增业务域%20-%20Domain%20与%20gen%20domain.md) §3 同一手法）。`appkit.Providers(ps ...Provider)` 可把多个 Provider 打包为一个 Option。
- **Plan 必须纯**：只读契约与声明，不写文件、不发网络、不改全局、不构造实例；返回的节点须带 `Key`/`Phase`（`Segments` 缺省取 `Segments()`，`Kind`/`Origin`/`Reload`/`EffectName` 由引擎补齐）。M1 起每个 Provider 恰好一个节点；`Priority` 用 `appkit.Priority*` 常量声明同阶段次序。
- **Build 输入 ApplyScope**：Config（阶段 B / server 阶段为已装载定型的完整契约；config 段构造期与 logger 阶段 A 可为 nil）、Decl（只读约定）、App（阶段 B 起非 nil，**前序段产物已填充**——audit 后端经 `storegorm.SQL.Resource(app)` 取连接即依赖此序）、Node（正在应用的节点）。
- **Build 产物形状按顶层段约定**：registry 段为 registry.Registrar；资源域（database/cache/storage/workflow/broker）与 instrumentation 段为 map[string]any（键=子段名，instrumentation 的键即后端名 prometheus/otlp/datadog，落 `instrumentation.<后端>`）；audit 段为 audit.Auditor；server 段为 []transport.Server；config 段为 []config.Layer；表外段同构（client 段的键是 `<面>.<名字>`，落 `client.grpc.<名字>` / `client.http.<名字>`，消费侧经 `appkit.NewKey` 句柄按名字取）；无产物可返回 nil。

## 2. 三种接入粒度

### 2.1 标准能力：直接用 contract.AppProvider()

标准能力包已导出现成 Provider（段路径逐个对过 `Segments()`）：

| 能力 | 构造 | 消费段 |
| --- | --- | --- |
| cache/redis | `rediscontract.AppProvider()` | cache.redis |
| broker/redis | `rediscontract.AppProvider()` | broker.redis |
| registry/nacos、registry/etcd | `contract.AppProvider()` | registry.nacos / registry.etcd |
| contrib/store-gorm | `storegorm.AppProvider(opts ...Option)` | database.sql（Option 透传） |
| contrib/observability-otlp | `otlpcontract.AppProviders(serviceName)` | tracer / metrics |
| contrib/config-nacos | `confignacos.AppProvider()` | config（nacos 源） |
| transport/{sse,cron,asynq} | `contract.AppProvider(opts ...Option)` | server.sse / server.cron / server.asynq |
| transport/{tcp,websocket,http3} | `contract.AppProvider(opts ...Option)` | server.tcp / server.websocket / server.http3 |
| transport/{graphql,webrtc,mcp} | `contract.AppProvider(opts ...Option)` | server.graphql / server.webrtc / server.mcp |
| transport/{kafka,rabbitmq,redis,rocketmq} | `contract.AppProvider(opts ...Option)` | server.kafka / server.rabbitmq / server.redis_server / server.rocketmq |
| transport/gateway | `contract.AppProvider(register RegisterFunc)` | server.gateway |
| oss/{s3,minio} | `contract.AppProvider()` | storage.s3 / storage.minio |
| metrics/{prometheus,otel,datadog} | `<后端>contract.AppProvider()` | instrumentation.prometheus / instrumentation.otlp / instrumentation.datadog |

接入三步（以 redis 缓存为例，与 [Bald 缓存使用](./Bald%20缓存使用.md) 同构）：引依赖 → MustServe 变参接一行 → yaml 声明段：

```go
appkit.MustServe(ctx,
    std.Standard(),
    rediscontract.AppProvider(), // 契约 cache.redis 段 → 资源面
    // ...业务域与其余 Option
)
```

```yaml
cache:
  redis:
    addr: "localhost:6379"
```

以 cache/redis 为例的内部语义：段存在 → 校验 / 建连 / Ping fail-fast（不产半成品实例）→ 产物落资源面 → cleanup 关闭底层连接池。落 contract 包的原因：标准能力包被 root 反向依赖会成环（导出范式：落 contract 包、导出名 AppProvider——既有标识符占用）。

服务器段（`server.*`）多一条取用口径：**路由只能在 `WithAfterBuild` 里挂**。`Build` 只构造 `*Server`（监听在 `Start` 才发生），产物随后进引擎的服务器列表，业务代码没有第二次拿到它的机会——所以句柄型接线是契约工厂的唯一入口：

```go
appkit.MustServe(ctx,
    std.Standard(),
    tcpcontract.AppProvider(
        tcpcontract.WithAfterBuild(func(_ context.Context, s *tcp.Server) error {
            s.RegisterMessageHandler(MsgTypePing, handlePing, nil) // nil Creator ⇒ handler 收到原始 []byte
            return nil
        }),
    ),
)
```

```yaml
server:
  tcp:
    addr: "0.0.0.0:9000"
```

三段各自的口径差异（都是实现现状，不是设计选择）：

- `server.tcp` / `server.http3`：`addr` 必给，业务侧 `tcp.WithAddress` / `http3.WithAddress` 在契约 Option 之后追加，因此**业务覆盖契约**。`server.http3` 的 `addr` 留空不会报错，而是被本仓 `transport/http3` 自己 `(*Server).init` 里写死的 `":443"` 接住（同一处也是无 `tlsConf` 时的自签来源），所以工厂在构造期就 fail-fast，文案里点名 `:443`。
- `server.websocket`：`addr` 是 `websocket.NewServer` 的**位置参数**，`WithServerOptions` 里没有覆盖通道 ⇒ 契约段是地址的唯一来源；`path` 留空时工厂**不追加** `WithPath`（恒传空串会把默认 `/ws` 顶成空路径），非空才追加。
- `tls` 三面同构（`file` 轴给证书路径、`config` 轴给内联 PEM、`ca` 轴开双向认证），且**空 `tls: {}` 段恒为报错**——「写了 tls 却没证书」不能退化成明文监听。唯一例外是 `server.http3` 完全省略 `tls`：QUIC 没有明文选项，实现自带一次性自签证书（生产必须显式给 `tls`）。

第二批（`transport/{graphql,webrtc,mcp}/contract`，2026-11-06）各自的口径差异：

- `server.graphql`：只有 `addr`（必填，`NewServer` 位置参数直进 `net.Listen`，空串会绑到全部网卡的随机端口）与 `tls` 两个字段；schema 路由经 `WithAfterBuild(func(ctx, *graphql.Server) error)` 追加 `Handle`/`HandleFunc`（注册表在 `NewServer` 时冻结，构造前挂不了，构造后、`Start` 前正是这个钩子的时点）。
- `server.webrtc`：`addr` 必填；`path` 默认 `/signal`。两个取用方必须知道的形状：① `Endpoint()` 返回的 URL **已把 path 嵌进去**（`http://<host>:<port>/signal`），再拼一次就 404；② `Start` 阻塞在 `Serve`，ctx 取消不解除阻塞，停机靠 `Stop`——`AppProvider` 返回的清理闭包已经代劳，直接用手搓 `Provider()` 的调用方要自己接。`codec` 写的是编码注册表的**键名**：该 codec 必须已被业务进程注册过，否则构造期报错。注册是显式的（`encoding.MustRegister(jsoncodec.New())`，在 `init` 或 `main` 早年段跑掉都行）——`encoding/json` 包**不会**因 import 而自注册，所以「YAML 写了 `codec: json`、代码里没人注册」是最常见的第一次失败。
- `server.mcp`：字段是 `address`（必填）、`name`、`version`。契约**没有 type 字段，工厂强制 HTTP（streamable）**——实现默认 STDIO 不绑任何端口，`address` 会被整个忽略；要 STDIO 就别开这个段（或经 `WithServerOptions(mcp.WithMCPServeType(mcp.ServerTypeStdio))` 显式改回，那时 address 不再决定落点，属业务自担）。`name`/`version` 留空回落实现默认（"MCP Server"/"1.0.0"）而不是置空——MCP 的 `initialize` 响应里 serverInfo.name 是客户端展示必填。工具注册走 `WithAfterBuild` + `RegisterHandlerWithJsonString`，客户端在 `POST <address>/mcp`。契约没有 `tls` 字段（底层 HTTP 层也没有证书通道，两边一致，不是缺口）。

```yaml
server:
  graphql:
    addr: "127.0.0.1:0"
  webrtc:
    addr: "127.0.0.1:0"
    path: /signal
  mcp:
    address: "127.0.0.1:0"
    name: my-mcp
```

第三批（`transport/{kafka,rabbitmq,redis,rocketmq}/contract`，2026-11-06）是四个 broker 型服务器面，与前两批同模板、不同接线形状：**工厂不代劳订阅**，`Start` 消费的是订阅/消费者注册表，所以业务侧的 handler 只能在 `WithAfterBuild` 里登记（`Build` 只构造 `*Server`，引擎随后直接 `Start`）：

```go
appkit.MustServe(ctx,
    std.Standard(),
    kafkacontract.AppProvider(
        kafkacontract.WithAfterBuild(func(ctx context.Context, s *kafka.Server) error {
            // 签名四族各不相同：kafka 带 groupName 与 disableAutoAck
            return s.RegisterSubscriber(ctx, "orders", "g1", false, handle, func() any { return &Order{} })
        }),
    ),
)
```

```yaml
server:
  kafka:
    addrs: ["127.0.0.1:9092"]
  rabbitmq:
    addrs: ["amqp://127.0.0.1:5672"]
  redis_server:
    addr: "redis://127.0.0.1:6379"
  rocketmq:
    name_server_addrs: ["127.0.0.1:9876"]
    driver_type: v2      # 只收 "" 或 v2：v5/aliyun 没有代码支撑，构造期报错
```

四族各自的口径差异（实现现状，不是设计选择；`redis_server` 是段名，别写成 `redis`——`broker.redis` 是另一段，那是客户端/资源面）：

- 四族共同：地址**必填 fail-fast**（配了段却不给地址，落点就不由配置决定）；`codec` 三态——留空＝沿用 broker 默认 `json`，写了未注册的名字＝构造期报错（底层 `WithCodec` 对未注册名静默置 nil，第一包才炸，那个失败点离配置太远）；`WithServerOptions` 追加在契约 Option 之后，因此**业务覆盖契约**；`Endpoint()` 三态（构造 `""` → `Start` 后描述性 → `Stop` 后回 `""`），引擎的就绪闸门读的就是这个面，含凭证的 URL 会先脱敏（redis/rabbitmq）。
- `server.kafka`：`addrs` 是列表且逐元素非空。kafka 的 `Connect` 只翻标志位，所以**没有集群时 `Start` 也绿**（订阅 flush 是懒成功，生产者协程后台重试）——「绿」在这里不代表连上了，取用方要等 `Endpoint()` 而不是等 `Start` 返回值。
- `server.redis_server`：`addr` 是单个字符串（`redis://host:port`，也接受带 `user:pass` 的 URL——密码不会出现在端点面上）。pubsub 的 `Connect` 是**真拨号 + PING 探针**，所以后端不在时 `Start` 就红，这一族没有「绿但没连上」的形状。
- `server.rocketmq`：`name_server_addrs` 必填；`driver_type` 只接受 `""` 与 `v2`——proto 注释里那句「默认 v5」后面**没有代码**（`transport/rocketmq` 恒构造 v2 broker），所以配 `v5`/`aliyun` 是构造期报错而不是静默按 v2 跑。`Connect` 同样只翻标志位，但订阅走 SDK 的同步启动（拉路由要真 NameServer），因此**带订阅者的 `Start` 在没有 NameServer 时必红**——与前两条不同形，别按 kafka 的直觉预期它绿。
- `server.rabbitmq`：`addrs` 必填且**逐元素非空**——broker 的 `Init` 会丢掉空元素，全空时整体换成内置的 `amqp://guest:guest@127.0.0.1:5672`，那是「写了配置却没落」的最坏形状。裸 `host:port` 可以（`Init` 补 `amqp://`）。另有一条实现事实：`Connect` **只拨 `addrs[0]`**，列表其余元素在这个后端是惰的（不像 kafka 那样多节点容错）。AMQP 的 `Connect` 是完整握手，不能像 kafka 那样在没有 broker 时绿。

### 2.2 非服务器后端：工厂在函数里，注册要显式

`std.Standard()` 与 §2.1 的 `*contract.AppProvider` 覆盖 **server 面**，以及日志/指标/追踪/注册中心/对象存储这些标准装配。下面 13 个 `contract` 包**不在默认集里**：它们导出的是「段 → 构造」的映射函数，必须由你的 app 在声明里包一层 Provider 才会生效。不注册不会崩（段没有消费者时按「无消费者」处理），所以**漏接的表现通常是「配置写了但后端不存在」**。

| 契约段 | 包 | 导出 | 形状 |
|---|---|---|---|
| `cache.local` | `cache/local/contract` | `Provider` | `func(ctx, *bootstrapv1.Cache) (any, func(), error)` |
| `database.mongodb` | `contrib/database/mongodb/contract` | `Provider` | `func(ctx, *bootstrapv1.Database) (any, func(), error)` |
| `workflow.argo` | `workflow/argo/contract` | `Provider` | `func(ctx, *bootstrapv1.Workflow) (any, func(), error)` |
| `broker.kafka` / `broker.rabbitmq` / `broker.rocketmq` | `broker/{kafka,rabbitmq,rocketmq}/contract` | `Provider` | `func(ctx, *bootstrapv1.Broker) (any, func(), error)` |
| `logger.backends[i]`（五个远端后端） | `log/{aliyun,charm,loki,sentry,tencent}/contract` | `Provider` | `func(ctx, *bootstrapv1.Logger_Backend) (log.Logger, func(), error)` |
| `audit.store` | `contrib/audit-gorm/contract` | `NewStoreProvider` | `func(*gorm.DB) appkit.AuditProvider` |
| `audit.stream` | `contrib/audit-stream/contract` | `NewStreamProvider` | `func(redis.UniversalClient) appkit.AuditProvider` |

前六行的写法一样，只是段名与工厂不同。`Segments`/`Apply`/`Plan` 按 §2.3 的范本写，`Build` 里调工厂：

```go
func (p kafkaProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
    b, closeFn, err := kafkacontract.Provider(ctx, s.Config.GetBroker())
    if err != nil {
        return nil, nil, err
    }
    return b, appkit.Cleanup(func(context.Context) error {
        if closeFn != nil {
            closeFn()
        }
        return nil
    }), nil
}
```

远端日志后端不走这条：`logger` 段由引擎按两阶段协议调用（§2.3 的 loggerProvider 是范本），在那个 Provider 的 `Build` 里按 `logger.backends[i].type` 分派到对应 `log/*/contract.Provider`。

审计两行的姿势见 `bald-admin` 的 `cmd/bald-admin/app/audit_wiring.go`：`audit.NamedBackend` 的 `New` 闭包里调用 provider——`NewStreamProvider(rdb)` 的入参取自资源面（`storegorm.SQL.Resource(app)` / Redis 契约客户端），返回的 `appkit.AuditProvider` 再用 `p(ctx, acfg, app)` 构造后端。

**同族子段漏接现在会报错**：`pkg/appkit` 的 `familySubSegmentProblems` 把「同族只点亮一个子段、其余子段静默」的判据从 `server` 推广到了全部非 server 族（`broker`/`cache`/`database`/`workflow`/`audit`/`config`/`log` 等）。例如只注册 `broker.redis` 却写了 `broker.kafka:`，计划期就会点名 `broker.kafka is configured but has no implementation`。确实尚未实现的子段，用 `appkit.WithUnimplementedSegments("broker.nats", …)` 显式声明豁免，别靠「反正会被忽略」活着。

### 2.3 整段自持：自己实现 Provider

范本 `_example/bald/internal/bootstrap/providers.go` 的 loggerProvider（覆盖 std 默认日志通道，行为测试钉住掩码语义）——三件套逐条对账：

```go
func (p loggerProvider) Segments() []string { return []string{"logger"} }              // 段路径

func (p loggerProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) } // Option 同款 Apply

func (p loggerProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
    return []appkit.Node{{Key: "logger", Kind: "logger", Segments: []string{"logger"},
        Phase: appkit.PhaseBootstrap, Priority: appkit.PriorityLogger,
        Reload: appkit.ReloadReconcile, EffectName: "appkit:bootstrap-logger"}}, nil
}

func (p loggerProvider) Build(_ context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
    l := s.Config.GetLogger()
    if l == nil { // 阶段 A（契约装载前）：引导 bslog——启动期日志仍带脱敏
        return bslog.New(baldbootstrap.LogOptions(nil), p.deco...), nil, nil
    }
    // 阶段 B/热更：按 logger.backends 逐项构造（非 slog 项 fail-fast 教学报错），
    // 多项经 log.NewMultiLogger 合并、契约 filter_keys 出口再包一层 log.NewFilterLogger。
    ...
    return log.NewFilterLogger(out, l.GetFilterKeys()...),
        appkit.Cleanup(func(context.Context) error { merged(); return nil }), nil // cleanup 聚合
}
```

声明序在 std.Standard() 之后 → 段路径末者胜，std 默认被整体接管。没有 init() 自注册——接入必须显式出现在你的代码里。

### 2.4 组合：多后端组装用 audit.Compose

Provider 的 Build 里要组装多个后端时，用 `audit.Compose`（`pkg/audit/compose.go`）：按声明序构造（顺序即广播序）；任一单元失败（重复名或构造出错）逆序回滚已构造单元的 cleanup，返回 `*ComposeError` 定位失败单元；多后端经 NewMultiAuditor 合并且 cleanup 聚合为逆序回放；单后端直接返回（类型断言友好）；空列表返回 NopAuditor。失败回滚与引擎账本同构（§5）。

## 3. 段语义：末者胜、缺段跳过、无消费者报错

- **同段末者胜**：引擎按精确段路径解析声明，后声明的 Provider 接管同一路径（std 默认后端由此可被覆盖）；不同路径并存（server.http 与 server.asynq 可由不同 Provider 同时供给）。
- **缺段 = skip**：Build 仅在至少一条声明路径实际出现于契约时被调用——段缺失是合法语义（不产半成品）；段在而 Build 失败 = fail-fast，启动中止。config 段例外：无条件构建（其源可独立于契约 config 段存在，如本地文件）。
- **构建时机按顶层段归类**：config 段构造期；server 段迟后阶段（业务 BeforeStart 之后——服务器构造消费业务建好的运行期资源）；logger 段走专用通道（不在 Provider 循环里——两阶段重建 + 热更协议，见 §6）；其余段在阶段 B 框架钩子内按计划序执行，同阶段次序由各 Provider `Plan` 声明的 `Priority` 给出（内置约定：logger → registry → tracer → metrics → database → cache → storage → audit → workflow → broker）。
- **声明无消费者 = L3 fail-fast**：契约里实际出现的顶层段若没有任何 Provider 消费，启动期直接报错（检查实现在 `pkg/appkit/segment_coverage.go`）：

```text
appkit: bootstrap config declares sections with no consumer:
  top-level sections with no consumer: script
  hint: 该段无消费者，配置不会生效；引入对应 Provider、经 appkit.WithUnimplementedSegments 显式豁免，或从契约移除该段
```

  server 段双层判定：非内置子段（http/grpc 之外）无 Provider 接管同样报错；用户改过 server 子字段却零能力声明也报错（与 bconf 预填默认深相等则豁免——框架自填非用户意图）。段名拼错由同一次检查覆盖：拼错 ⇒ 该段无人覆盖。这条检查的由来是 script 段教训——全仓零消费者，用户配置静默失效。
- **白名单出口**：`appkit.WithUnimplementedSegments("script", "server.asynq")`——契约先行的工位显式承认「该段暂不生效」，多次调用追加；条目为顶层段名或 server 子段路径，**必须是 `BootstrapConfig` 里真实存在的消息型字段链**（拼错在计划期报错，不会静默制造一个不存在的豁免）。它是唯一豁免出口。

## 4. 产物落资源面

引擎把 Build 产物按段路径落进统一资源面（`bootstrap.go`）：map[string]any 产物按 `<顶层段>.<子段>` 逐键展开（nil 值跳过），非 map 产物整体落 `<顶层段>`；仅阶段 B 单线程写入，Run 后只读。取用走类型化句柄 `Key[T].Resource(app)`（字符串入口 `App.Resource(string)` 已按 B2 删除）：

- **子段精确路径直返实例**：`storegorm.SQL.Resource(app)` → `*gorm.DB`（类型在句柄上静态确定，无断言）；
- 顶层段（"cache"）→ map[string]any 副本快照（键=子段名，可安全遍历/留存）；用 `appkit.NewKey[map[string]any]("cache").Resource(app)` 取；
- registry/audit 等整产物段 → 产物本身；
- 未装配或段缺失 → `(nil, false)`；装配构造期恒 `(nil, false)`。

域 Wire、业务钩子都从这一个入口取（[新增业务域](./新增业务域%20-%20Domain%20与%20gen%20domain.md) §6：基础设施经资源面迟到取用）。tracer/metrics 产物刻意不落资源面（shutdown/暴露端由 Provider 自持，cleanup 槽位承担停机回收）；六资源域段（database/cache/storage/ai/workflow/broker）不支持热更新。

## 5. cleanup 与停机：Effect 账本

- Cleanup 签名统一为 `func(context.Context) error`（旧无参 func() 经 noopCleanup 适配——Provider 实现侧的便利件）。
- 每个 Provider 的 cleanup 按计划推导的停机序**预登记 Effect**（槽位模式，Build 成功后填充）：audit-restore 注册最早 → 逆序回放时最后执行（stream 尾批审计事件在指标/span flush 之后收集）；未声明依赖的第三方 Provider 的 cleanup 追加在计划序之后——回放更早，业务资源先收、框架底座后收。
- **Run 的回滚账本语义**：构建一个、入库一个（逐项即时落账——阶段 B 后段 Provider 依赖前段产物）；任一钩子/构建失败，Run 失败路径 UndoEffects 逆序回放已登记效应（幂等、清空账本），构建失败不在此重复回滚；优雅停机 stopAll 走同一账本逆序回放。

## 6. std.Standard() 的三个内置与顶替

`std.Standard()` 打包框架自有后端的默认 Provider（`pkg/appkit/std/std.go`，实现在 `pkg/appkit/standard.go`）：

| 内置 | 段路径 | 说明 |
| --- | --- | --- |
| StandardHTTPProvider / StandardGRPCProvider | server.http / server.grpc（各一个 Provider） | 能力声明在代码（`WithHTTP`/`WithHTTPContract` 与 `WithGRPC` + `WithGRPCOptions`/`WithGRPCOptionsContract`——钩子形态与直给形态写进同一个 `Decl` 字段，Provider 看不出区别），契约段驱动地址/TLS；迟后阶段构建。分成两个是为了让第三方按段接管其中一面——转码面即 `transport/gateway/contract` 的 `HTTPProvider` 接管 `server.http`，gRPC 面不受影响 |
| StandardAuditProvider | audit | audit.backends 的 "log" 项：结构化日志审计后端，核心包零依赖开箱即用 |
| StandardLoggerProvider | logger | bslog（标准库 slog）——阶段 A 引导 logger 的持有者；两阶段重建 + 热更协议仍在引擎 |

其余各段（registry/database/cache/storage/workflow/broker/tracer/metrics/config）无默认接线：段存在而无 Provider = L3 启动期报错（§3）。

**顶替方式即末者胜**：声明同段路径的 Provider 写在 std.Standard() 之后即整体接管——audit 段被用户 audit Provider 接管后，store/stream 等第三方后端按自己的语义装配；logger 段特殊：不在 Provider 循环里、由引擎按两阶段 + 热更协议调用，但覆盖仍是同一条末者胜规则（§2.3 的 loggerProvider 即范本——阶段 A 引导日志也带你的装饰）。server.http/server.grpc 按能力声明装配，行为细节见各能力使用页。

## 报错速查

| 报错 | 根因与修复 |
| --- | --- |
| `appkit: provider main.loggerProvider declares no segments (Segments must not be empty)` | Segments() 返回空——至少声明一条段路径 |
| `appkit: provider … declares an empty segment path` | 段路径列表里混入空串 |
| `appkit: provider [logger server.http] declares segments across build phases … — split it into one Provider per phase` | 同一 Provider 声明了跨构建时机的段路径——按阶段拆成多个 Provider |
| `appkit: 2 providers declare logger segments ([logger logger]); only one logger provider may be declared` | logger 段至多一个 Provider（专用通道） |
| `appkit: bootstrap config declares sections with no consumer: …` | 顶层段无消费者——引 Provider / 白名单 / 移除段（§3） |
| `server.asynq is configured but has no implementation in this repo (remove the section, provide a Provider for "server.asynq", or whitelist it via appkit.WithUnimplementedSegments)` | server 非内置子段无接管 |
| `appkit: provider [cache.redis] returned *T, want map[string]any` | 六资源域段产物必须是 map[string]any（键=子段名） |

## 边界与注意事项

- 没有 init() 自注册——未 import 的后端零编译成本、依赖图可见；第三方后端一律不进 std（std 只打包框架自有后端，对账基准「不多不少」）。
- 声明解析期 fail-fast 的三类错误见报错速查前三行——都在启动早期，不产半成品实例。
- 段覆盖检查在契约装载定型后的 BeforeStart 装配链末尾执行；热更路径不重跑该检查。

## FAQ

- **Provider 和 Option 什么关系？** Provider 嵌入 Option，Apply 把自身追加进声明——Provider 值就是 Option，可直接作为 MustServe 变参项。
- **同一段两个 Provider 谁生效？** 精确路径末者胜，后声明者接管；被顶替者不参与构建。
- **怎么临时豁免框架还没实现的段？** WithUnimplementedSegments 白名单（§3）。
- **表外（第三方）段产物怎么取？** 同一约定：map 产物按 `<顶层段>.<子段>` 展开、非 map 产物落 `<顶层段>`；取用走类型化句柄 `appkit.NewKey[T](path).Resource(app)`（§4）。

