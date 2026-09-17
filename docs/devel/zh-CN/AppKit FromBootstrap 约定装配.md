# AppKit FromBootstrap 约定装配

> 2026-09-06 新增。`pkg/appkit/bootstrap.go`（契约约定装配入口）+ `bootstrap_test.go`。
> 纯增量：不改动 `New`/`Option` 既有语义，e2e 的 newApp+Option 覆盖机制不受影响。

## 动机

`appkit.New` 是显式 Option 装配，业务 main.go 需要手写 Bind×3、BeforeStart 装载+
校验+重建 Logger、OnConfigChange 再装载等样板（示例 main.go 曾达 500 行）。
这些样板对应的「形状」其实早已在契约 `BootstrapConfig` 里（app/server/logger/config
段），属于框架该内化的约定。

## 分工边界（配置驱动参数，代码声明能力）

| 配置驱动（契约 / flag / env / 文件） | 代码声明（配置表达不了） |
|---|---|
| app 元数据（id/name/version/env/stop_timeout） | gin 路由与业务 handler |
| server 地址 / TLS / 停机超时 | gRPC service 注册 |
| 日志级别/格式/输出（热更新即改即生效） | 拦截器链序（安全策略：ErrorInterceptor 最外层） |
| 注册中心实例 / 配置源层 | 就绪探针的下游依赖 |
| 热更新开关 | 日志装饰器（脱敏）、gateway 转码注册回调 |

上表切的是「参数 vs 能力」；另一刀切在**模块归属**：启动必需品归 bootstrap，
业务运行期资源归 appkit。bootstrap 只装 Config/Logger/Server 三段——进程
站起来的前提（缺配置拿不到参数、缺日志无法观测启动期、缺服务器进程没有
存在意义）；appkit 的九类 Registry（Database/Cache/Storage/Ai/Broker/
Workflow/Tracer/Metrics/Registrar）管业务运行期的资源接线，经
`With*Registry` 显式装配、生命周期挂 Effect。判定口诀：问「进程能不能没有
它起来」——能，归 appkit；不能，才进 bootstrap。细节见《Bald Bootstrap
设计.md》「与 appkit 的协作」。

## 装配路径选择（何时用 FromBootstrap，何时用 New）

框架提供两条语义等价的装配入口（`buildRegistrar` 等 builder 两侧同实现，
无双真相源）：

| | `FromBootstrap(cfg, opts...)` | `appkit.New(opts...)` + 手动装配 |
|---|---|---|
| 适合 | **绝大多数业务**：契约驱动，Bind/装载/校验/Registry/热更新全部内化 | 需要精细控制组件生命周期、自定义装配时序、契约形状表达不了的组装 |
| 用户侧样板 | ~30-60 行（Option 声明 + handler 注册） | ~200+ 行（Bind×3、BeforeStart 装载、Registry 手动 Build、Effect 手动挂） |
| 典型消费者 | `_example/bald`（quickstart，433 行/13 包）；go-bald-admin（reference，U1 已切，630 行——数据段经透传 provider 消费） | （无——业务自有桥接/数据段透传场景经 With*Registry 表达，见下「透传 provider」） |

**默认走 FromBootstrap**。选 New 的判定信号：需要把 Registry.Build 拆到
BeforeStart 的自定义时序点、组件间有 FromBootstrap 表达不了的依赖编排、
或需要多个 app 实例共存。走 New 时注意三个已知泄漏点（FromBootstrap 已
内化）：serviceName 闭包须 Bind 时取值（构造期取值有时序问题）、Effect
注册顺序决定停机逆序、BeforeStart 闭包内 `:=` 会遮蔽外层 cleanup 变量
（必须 `=`）。

**重业务透传（U1，v0.2.6）**：生命周期面经 `WithBeforeStart/WithBeforeStop/
WithEffect/WithReconcile/WithOnKeyChange/WithProvides/WithRequires/
WithComponents` 转发给 New 的同名 Option（钩子在框架装载链之后执行、
Effect 逆序回放先于框架 Effect——业务资源先收）。**业务自管数据段**经
`WithDatabaseRegistry/WithCacheRegistry/WithStorageRegistry` 注册透传
provider 消费（构造语义保留业务 env 优先级与降级，连接生命周期上挂框架
Effect；实例经 `app.Database/Cache/Storage` 取回注入）——go-bald-admin
的 DB/Redis/MinIO 桥接即此路径，与 contrib contract 官方 provider 并存
（两条合法路径，代码声明能力）。

原则：**能力声明在代码**。契约有 server.grpc 段但业务未 `WithGRPC` 时，对应 flag
变更不产生效果（没有 server 消费）——这是刻意的，避免「配置说开了、没人实现」
的静默失效（与 S1 能力声明 fail-fast 同哲学）。

## FromBootstrap 内化的约定

1. **App 元数据**取自契约 app 段（空值回退默认：name=bald-app、version=v0.0.0、
   stopTimeout=30s）。
2. **Bind**：`server.http`/`server.grpc` 段非 nil 即 Bind（flag 可覆盖契约）；
   `--log.*` flag 壳（`Bind("", LogOptions(nil))`）只注册定义，终值以契约为准。
3. **日志两阶段**：阶段 A（构造期，默认 Logger，保证契约装载前有日志）→
   阶段 B（BeforeStart 装载+校验后按契约 logger 段重建）→ 停机经 Effect 恢复
   原 Logger 并释放后端。业务装饰器（脱敏）阶段 A/B 统一生效。
   （两级工厂分发与 deco 分级的设计论证见《AppKit 日志装配设计.md》。）
4. **ConfigRegistry**：契约 Config 段经 `bootstrap.Registry.Build` 产出配置层
   （注册序=层优先级），cleanup 挂 Effect 停机释放。
5. **BeforeStart**：Settings→Unmarshal→Validate→rebuildLogger。
6. **服务器构造走 `bootstrap.ServerRegistry`**：能力声明（WithHTTP/WithGRPC/
   WithGatewayRegister）→ provider 注入，契约 server 段 → `BuildServers` 装配，
   与直用 bootstrap 的路径同一实现（消除双真相源）。provider cleanup 挂
   Effect（"appkit:servers"）；BuildServers 失败回滚阶段 A 与配置层。
7. **OnConfigChange（热更新）**：副本试装载（proto.Clone）+ Validate + 整契约
   原子落盘（`*cfg = *candidate`，字段是子消息指针、整体替换无别名问题）+
   重建 Logger。坏配置降级记日志、保留旧契约（与审计旁路同哲学）。
   ——Unmarshal 只做结构转换不校验取值，坏值必须由 Validate 拦在落盘之前
   （单测 TestHotReload_BadConfigKeepsOld 钉死该语义）。
8. **可观测性双 Registry**（2026-09-12）：契约 `tracer`/`metrics` 段经
   `TracerRegistry`/`MetricsRegistry` 装配（BeforeStart 在 registrar 之后、
   database 之前）；metrics 暴露端独立于业务 server（缺省 `:9091`）；停机
   flush Effect 注册在 bootstrap-logger 之前——逆序回放**最后**执行，
   所有组件 cleanup 后再 flush 尾批指标与 span。

## API 速查

```go
app, err := appkit.FromBootstrap(bootstrap,
    appkit.WithHTTP(router),                       // 业务 handler 必供
    appkit.WithGRPC(register, serverOpts...),      // service + 完整拦截器链
    appkit.WithHealth(healthChecker),              // 缺省不挂探针（协议层也不再兜底）
    appkit.WithRegistrar(inmemory.New()),
    appkit.WithConfigFile("configs/bald-demo.yaml"),
    appkit.WithWatchConfig(true),
    appkit.WithLogDecorators(deco...),             // 脱敏等
    appkit.WithAfterStart(fn),
    appkit.WithGatewayRegister(fn),                // gateway 转码能力（driver=grpc-gateway 选网关面）
    appkit.WithConfigRegistry(reg),                // 契约 Config 段层装配
    appkit.WithRemoteConfig(src),                  // kratos 桥远程源
    appkit.WithLogRegistry(reg),                   // 契约驱动日志后端（logger.backends 逐项查表；log/<backend>/contract 注册；不声明时默认路径仅 slog 项 + 教学报错）
    appkit.WithDatabaseRegistry(dbReg),            // 契约驱动数据库客户端（database.<engine> 段查表；contrib/database/<engine>/contract 注册）
    appkit.WithCacheRegistry(cacheReg),            // 契约驱动缓存实例（cache.<backend> 段查表；cache/<backend>/contract 注册）
    appkit.WithStorageRegistry(storageReg),        // 契约驱动对象存储（storage.<backend> 段查表；oss/<backend>/contract 注册）
    appkit.WithAiRegistry(aiReg),                  // 契约驱动 AI 客户端（ai.<backend> 段查表；ai/<backend>/contract 注册）
    appkit.WithTracerRegistry(trReg),              // 契约驱动 tracer（tracer 段查表；observability-otlp/contract 注册）
    appkit.WithMetricsRegistry(mReg),              // 契约驱动 metrics（metrics 段查表；含独立暴露端缺省 :9091）
)

// 数据库客户端（阶段 B 装配，Run 后取用；类型安全在消费侧恢复）：
//   dr := bootstrap.NewDatabaseRegistry()            // ← Registry 在 bootstrap（2026-09-15 迁入）
//   dr.MustRegister(gormcontract.Type, gormcontract.Provider)    // "sql"
//   dr.MustRegister(mongocontract.Type, mongocontract.Provider)  // "mongodb"
//   cli := app.Database(gormcontract.Type).(*gormcrud.Client)    // ← 业务断言

// 缓存实例（与 DatabaseRegistry 同模式）：
//   cr := bootstrap.NewCacheRegistry()
//   cr.MustRegister(localcontract.Type, localcontract.Provider)  // "local"（freecache）
//   cr.MustRegister(rediscontract.Type, rediscontract.Provider)  // "redis"（自建 client，cleanup 关连接池）
//   c := app.Cache(rediscontract.Type).(cache.Cache)             // ← 业务断言
```

失败语义：`cfg=nil` / 能力声明与契约段缺失不匹配 → 构造期 fail-fast 返回 error；
`WithGatewayRegister` 与 `server.http.driver` 冲突（非 grpc-gateway 值，或与
WithHTTP 同时声明且留空）→ 构造期 fail-fast（显式 > 隐式）；ConfigRegistry
Build / BuildServers 失败 → 回滚阶段 A Logger 与配置层后返回 error；
`database.<engine>` 段存在但 Provider 未注册（或未接 DatabaseRegistry）→
阶段 B fail-fast——未 import 的引擎启动期报错，不静默无数据库。

### 数据库客户端（DatabaseRegistry 模式）

与 `bootstrap.RegistrarRegistry` 同模式（显式注册、段存在未注册 fail-fast、
cleanup 挂停机 Effect），两点差异要清楚：

- **多段并存**：database 是 optional 段集合（sql/mongodb/clickhouse/doris/
  elasticsearch/opensearch/influxdb/cassandra），主库+检索库可同时配置；
  装配语义是「每段各自构建、全部返回」（键=契约段名），与 registry 的
  type 单选不同。
- **any 边界**：引擎客户端异构，Provider 返回 any 是装配层的必然；appkit
  只保存与转发（`Database(typ)` / `Databases()`），不做断言。类型安全在
  消费侧恢复——SQL 客户端接 contrib/store-gorm 变 `store.DBProvider[T]`。
  分工：`contrib/database/<engine>` 管连接生命周期，store-* 管 CRUD 语义。

- **生命周期**：阶段 B（BeforeStart，契约装载校验后）构建；停机 Effect
  注册在 servers 之前——逆序回放保证「服务器先 drain、数据库连接最后关」；
  失败回滚已建实例 cleanup（逆序）。database 段不支持热更新（连接池重建
  侵入性大，变更需重启）。
- **分批落地**：首批 sql（bald-database-gorm）与 mongodb
  （bald-database-mongodb）；其余 6 段契约形状已定、后端按需补——未配置段
  未使用，不违反「契约字段须全有消费者」。
- **migrate 语义**：迁移模型是代码声明（`WithAutoMigrate` / mixin），契约
  不承载模型清单；契约 `migrate` 开关经 `WithEnableMigrate` 生效。go-wind
  的 `RegisterMigrateModel` 全局注册表 hack 不移植。

### 缓存实例（CacheRegistry 模式）

与 DatabaseRegistry 完全同模式（显式注册、段存在未注册 fail-fast、多段
并存、失败回滚逆序 cleanup、阶段 B 构建、段不支持热更新），差异仅两点：

- **停机顺序**：cache-clients Effect 注册在 database-clients **之后**——
  逆序回放 = 服务器 drain → **缓存先关**（加速层，关了不影响正确性）→
  数据库连接最后关。
- **redis 契约段自建 client**：`cache.redis` 段含 addr/password/db，contract
  Provider 自建 go-redis client（Ping 失败即 fail-fast，不产半成品），cleanup
  关连接池；复用注入场景绕过 contract 直接用 `cache/redis` 包的 `New(client)`。

- **与 `contrib/cache-redis` 的边界**：`bald/cache` 是通用 KV 缓存抽象
  （Get/Set/SetNX/Multi，进程内 freecache / 分布式 redis）；cache-redis 是
  带 loader 回填的 **Cache-Aside 旁路缓存组件**（P11 晋升，键须含租户）。
  关注点不同，互不替代——cache-redis 未来可长在 `bald/cache` 抽象之上。

### 网关转码面（gateway as driver 模式）

gateway 不是独立服务器，而是 `server.http` 段的一种模式，由契约
`server.http.driver` 驱动装配（复用 `HttpServerProvider` 的模式分支）：

- 声明 `WithGatewayRegister(fn)` + driver=`grpc-gateway`（或留空）→ HTTP
  端口即网关转码面（REST→gRPC），业务 handler 被替代；
- 与 `WithHTTP` 同时声明时 driver 必须显式为 `grpc-gateway`（留空会让两个
  能力静默竞争同一端口，fail-fast）；driver 为其他值 → 转码能力无消费面，fail-fast；
- 业务要混合路由时在 fn 里返回组合 handler（如 gin 主面 + NoRoute 落转码 mux）；
- `WithExtraServers` 保留为通用逃生舱（契约形状表达不了的服务器），
  gateway 不再走它。

### 工作流引擎与消息代理（WorkflowRegistry / BrokerRegistry 模式）

与 AiRegistry 完全同模式（显式注册、段存在未注册 fail-fast、多段并存、
失败回滚逆序 cleanup、阶段 B 构建、段不支持热更新），差异要点：

- **workflow.argo**：REST 客户端消费 `server_url/namespace/token/
  insecure_skip_verify`；`app.Workflow("argo")` 取 `*argo.WorkflowClient`。
- **broker 多段并存**：`broker.{kafka,rabbitmq,redis,rocketmq}` 四段独立
  构建并 `Init`→`Connect`（contract 封装），`app.Broker(typ)/Brokers()` 取用；
  消息体经 `bald/encoding` 序列化（默认 json）。binder 类型化依赖
  `broker.Unmarshal` 的 any 解引用修复（契约总览 §16）。
- **停机链序扩至 12 段**（注册序，逆序回放 = 后注册先撤销）：
  tracer-shutdown→metrics-server→bootstrap-logger→registrar→database→
  cache→storage→ai→workflow→broker→config-layers→servers。
  回放（关闭）序即其精确倒序：**servers 先 drain → config 层释放 →
  broker → workflow → ai → storage → cache → database → registrar →
  logger → metrics → tracer 最后 flush**。语义：依赖对称拆除（broker 等
  database 的消费方先关，database 后关）；tracer/metrics 的 Effect 注册在
  bootstrap-logger 之前——回放时**最后**执行（所有组件 cleanup 后再 flush
  尾批指标与 span）。

### 可观测性（TracerRegistry / MetricsRegistry 模式，2026-09-12）

与 `bootstrap.RegistrarRegistry` 单选模式同款（`tracer.type`/`metrics.type` 单选查表、
显式 MustRegister、type 空/未注册 fail-fast、段缺省 no-op），差异要点：

- **serviceName 闭包绑定**：contract Provider 构造器收 `func() string` 而非
  裸字符串——OTLP resource 属性在 Build 时才取契约 app.name，避免构造期
  取值时序问题。
- **metrics 双通道**：`metrics.type` 为 `prometheus`（仅本地抓取）或
  `otlp`（抓取+直推双通道，endpoint 必填）；暴露端独立于业务 server
  （`appkit.StartMetricsServer`，缺省 `:9091` `/metrics`，生命周期解耦——
  业务 server drain 时指标端仍在产出尾批数据）。
- **停机次序最晚**：tracer/metrics 的 flush Effect 注册在 bootstrap-logger
  之前——逆序回放时**最后**执行（所有组件 cleanup 后再 flush 尾批指标
  与 span）。
- **Provider 包**：`contrib/observability-otlp/contract`（`NewTracerProvider`/
  `NewPrometheusProvider`/`NewOTLPProvider`），只 import bconf；
  `obmetrics.Setup` 返回值扩为 `(recorder, shutdown, error)`（v0.2.0 签名
  变更，shutdown 挂 Effect）。
- **配套修复（v0.2.1）**：`pkg/metrics` instruments 改惰性创建（首次
  Record 时绑 meter）——根治 OTel global 语义下 SetMeterProvider 前构造
  的 instrument 永久 noop（详见《Bald 指标设计》「惰性 instruments」）。
- **云端终验（2026-09-12，Insight DCE 5.0）**：collector
  `10.82.138.249:32414` 直推双通道全通——VictoriaMetrics 见
  `bald_requests_total{job="go-bald-admin"}`（OTLP→Prometheus 的
  `service.name`→`job` 标签映射；**2026-09-15 semconv 对齐后该指标已退役**，
  现行查 `bald_audit_events_total` / `http_server_request_duration_seconds`），
  Jaeger 见 `POST /v1/login` span（`http.status_code=401` 完整保留）。
  export 零错误。

## 示例改造结果（_example/bald）

main.go 从 ~560 行降至 ~340 行（剩余几乎全是业务路由/handler 与教学注释），
装配段收缩为 newApp 内一组 BootstrapOption。HTTP 面按构建分叉：默认构建走
gin 演示路由，grpcgw 构建走网关转码面（`WithGatewayRegister` + yaml
`server.http.driver: grpc-gateway`）。e2e 为 `newApp(bootstrap, ready)`，
与生产共用同一构造函数（「测的=跑的」由函数复用保证，不再靠注释自觉）。

## 已知耦合与坑

- **env 前缀耦合 app.name**：bconfig Store 的 env 层用 `environMap(Name)` 生成
  前缀（bald-demo → BALD_DEMO_*）。Name 来自契约 app.name（构造期可见值），
  业务自定义 env 前缀必须**在 FromBootstrap 之前**设好契约 name
  （示例在 newApp 开头设 `bootstrap.GetApp().Name = "bald-demo"`）。
  漏设的症状：env 覆盖静默失效、文件值压过测试注入值（e2e 曾因此挂）。
- **example go.mod**：`pkg/middleware/gin` 传递依赖 `bald-crud/viewer`（嵌套
  module），example 需 `replace => ../../../bald-crud/viewer`。
- **nacos tag 的 SDK 版本分裂**：contrib registry/nacos/v3 用 v2 SDK（naming），
  contrib config/nacos/v3 用 v1 SDK（config）。两个 SDK 共存，server/client
  配置各用各的类型；register_nacos.go 已按此修正（原文件 naming client 误用
  v1 import，`-tags nacos` 本就编译不过）。

## 验证

- 单测 10 个（appkit/bootstrap_test.go）：元数据契约驱动、nil/段缺失 fail-fast、
  服务器构造数量、gateway driver 语义（段缺失/单声明默认转码面/双声明留空
  fail-fast/显式 grpc-gateway/其他值 fail-fast）、日志两阶段生命周期+停机恢复、
  ConfigRegistry 层装载+cleanup、双服务器动态端口 Run、热更新重建/坏配置保留。
- e2e（grpcgw tag）全绿：REST 面即 server.http 端口（网关转码面），与 gRPC
  共享同一份校验规则；默认/grpcgw/nacos 三种 tag 组合编译通过。
