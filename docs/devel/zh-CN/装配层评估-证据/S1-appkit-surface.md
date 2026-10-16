# S1 · Go 框架装配面审计（bald / pkg/appkit + 注册表）

范围：`D:\code\konglingfei\bald`（module `github.com/kalandramo/bald`，仓库内 93 个 `go.mod`）。
方法：只读。文件清单用 glob/Select-String；精读 30 个文件（appkit 非测试 20 个全读，
bootstrap 的 server.go/registrar.go、internal/codegen/appspec_gen.go、_example/bald/main.go、
_example/bald/user/*、3 篇设计文档）。未跑 git 写操作、未改任何仓库文件。

行号口径：**文件真实行号**（UTF-8）。注意本机 `Get-Content` 会以非 UTF-8 解码这些源文件，
行号与行数都会错（例如 appkit.go 被读成 589 行，实际 772 行），
本报告所有行数用 `[System.IO.File]::ReadAllLines(path, UTF8).Count` 取样。

---

## 1. 规模基线

| 项 | 值 | 证据 |
|---|---|---|
| appkit 非测试代码 | 20 文件 / **4052 行** | 逐文件计数见下表 |
| appkit 测试代码 | 24 文件 / **5150 行**（测试:生产 = **1.27:1**） | 同上 |
| appkit 导出符号 | **145**（func/method 128 + type 15 + var 2）+ 5 导出 const | Select-String `-CaseSensitive` 统计 |
| 全框架资源注册表 | **13 个**（bootstrap 10 + appkit 3）+ 泛型 `Registry[T]` | §3 |
| `FromBootstrap` 单函数体 | **454 行**（bootstrap.go:460–913） | 函数起止行 |
| `Run` 函数体 | 143 行（appkit.go:477–619）；`stopAll` 36 行（631–666） | 函数起止行 |

逐文件行数（非测试）：appkit.go 772 / bootstrap.go 1101 / reconcile.go 367 / observability.go 268 /
audit_registry.go 243 / mount.go 194 / component.go 116 / health.go 99 / capability.go 95 /
effect.go 92 / keywatch.go 91 / ai.go 78 / database.go 78 / broker.go 77 / storage.go 76 /
cache.go 74 / workflow.go 74 / doc.go 65 / registry.go 64 / harness.go 19 / types.go 9。

`_example/bald/main.go` 441 行（其中约 200 行是教学注释，业务装配段 = `newApp`，177–249 行）；
设计文档自述“main.go 从 ~560 行降至 ~340 行”（`docs/devel/zh-CN/AppKit FromBootstrap 约定装配.md:259`）。

---

## 2. appkit 导出 API 全清单（三分类）

分类口径：
- **必需** = 业务不写就起不来（跑 `FromBootstrap` 路径的最小集）。
- **条件必需** = 只在用某能力时必需，括号内写明是哪项能力。
- **泄漏** = 框架内部概念，用户不该看到但被迫看到（写明理由）。

### 2.1 类型（15）

| 符号 | 证据 | 分类 |
|---|---|---|
| `AppKit` | appkit.go:51 | 必需 |
| `Option` | appkit.go:249 | 条件必需（走 `New` 手工装配时） |
| `BootstrapOption` | bootstrap.go:160 | 必需（`FromBootstrap` 的唯一扩展面） |
| `HealthOption` | health.go:20 | 条件必需（健康检查探针路径/间隔） |
| `Component` | component.go:31 | 条件必需（C1 进程内组件：`Name/Start/Dispose` 三方法须自己实现） |
| `ReconcileFunc` | reconcile.go:54 | 条件必需（R1-2 期望态协调） |
| `ReconcileCtx` | reconcile.go:63 | 条件必需（同上）；**泄漏嫌疑**：把 K8s controller 的期望态/实际态模型塞进 App 层，用户必须理解 `Settings/Mounted/Has/Mount/Unmount` 五个概念 |
| `Registry[T]` | registry.go:18 | **泄漏**：泛型按名注册表。框架自身 13 个注册表**无一使用它**（各自手写 map+mutex，见 §3），全仓非测试代码中它只被 `mount.go:55/71` 的方法体引用 → 是无消费者的导出机器 |
| `PlainBinder` | appkit.go:187 | 条件必需（用 `Bind` 绑 flag 时）；**泄漏**：“注册无前缀 flag”的配置装载协议 |
| `TracerProvider` | observability.go:59 | 条件必需（tracer 段） |
| `TracerRegistry` | observability.go:63 | 条件必需（tracer 段） |
| `MetricsProvider` | observability.go:127 | 条件必需（metrics 段） |
| `MetricsRegistry` | observability.go:132 | 条件必需（metrics 段） |
| `AuditProvider` | audit_registry.go:76 | 条件必需（audit 段） |
| `AuditRegistry` | audit_registry.go:81 | 条件必需（audit 段） |

导出 const（5）：`DefaultHealthPath` / `DefaultReadyPath`（health.go:15–16，条件必需·健康检查）、
`AuditTypeLog` / `AuditTypeStore` / `AuditTypeStream`（audit_registry.go:53/55/58，条件必需·audit）。

导出 var（2）：`ErrAlreadyRunning`（appkit.go:48，必需——`Run` 防重入）、
`ErrNotRunning`（mount.go:40，条件必需·运行期挂载）。

### 2.2 入口与构造（8）

| 符号 | 证据 | 分类 |
|---|---|---|
| `FromBootstrap` | bootstrap.go:460 | 必需 |
| `New` | appkit.go:373 | 条件必需（手工装配路径） |
| `NewHarness` | harness.go:15 | **泄漏**：注释自陈“生产代码不应使用本装置”（harness.go:6），为跨 module 集成测试而导出 |
| `NewReconcileCtx` | reconcile.go:160 | **泄漏**：注释自陈“供测试或手动触发协调使用”（157–159 行） |
| `NewAuditRegistry` / `NewTracerRegistry` / `NewMetricsRegistry` | audit_registry.go:87 / observability.go:69 / observability.go:138 | 条件必需（对应能力段） |
| `NewRegistry[T]` | registry.go:24 | **泄漏**（同 `Registry[T]`） |

### 2.3 `FromBootstrap` 侧的 `With*` Option（37）

必需（1）：`WithConfigNamespace`（bootstrap.go:265）——**必填，缺则 `FromBootstrap` 直接返回错误**
（bootstrap.go:640–645）。

条件必需（36），按能力分组：

- 协议面：`WithHTTP`（163）、`WithGRPC`（184）、`WithGatewayRegister`（178）、`WithExtraServers`（225）、
  `WithExtraServerFunc`（239）、`WithServerRegistry`（383）、`WithGRPCOptions`（199）。
  其中 `WithGRPCOptions`（199）、`WithExtraServerFunc`（239）是**泄漏**：为“构造期拿不到运行期资源”
  这一框架时序问题提供的逃生舱（注释 188–198 / 229–238 自陈）。
- 注册中心：`WithRegistrar`（205）、`WithRegistrarRegistry`（212）。
- 配置：`WithConfigFile`（250）、`WithConfigRegistry`（276）、`WithRemoteConfig`（218）、
  `WithWatchConfig`（270）、`WithStrictContract`（298）、`WithBind`（450）。
- 日志：`WithLogRegistry`（329）、`WithLogDecorators`（308）。
- 数据/中间件域（6）：`WithDatabaseRegistry`（database.go:34）、`WithCacheRegistry`（cache.go:33）、
  `WithStorageRegistry`（storage.go:34）、`WithAiRegistry`（ai.go:37）、`WithWorkflowRegistry`（workflow.go:33）、
  `WithBrokerRegistry`（broker.go:36）。
- 可观测性与审计：`WithTracerRegistry`（337）、`WithMetricsRegistry`（345）、`WithAuditRegistry`（355）。
- 健康检查：`WithHealth`（health.go:49）+ `WithProbePaths`（health.go:23）/`WithHealthPollInterval`（health.go:35）。
- 机制透传（**泄漏**，把 New 侧的 5 类自研机制原样搬到第二入口，形成 12 组同义双拼）：
  `WithBeforeStart`（399）、`WithBeforeStop`（404）、`WithEffect`（410）、`WithReconcile`（416）、
  `WithOnKeyChange`（422）、`WithProvides`（428）、`WithRequires`（433）、`WithComponents`（439）、
  `WithAfterStart`（244）。

### 2.4 `New` 侧的 Option（28）

| 符号 | 证据 | 分类 |
|---|---|---|
| `Name` / `Version` / `ID` | appkit.go:252 / 253 / 251 | 条件必需（手工装配路径；`FromBootstrap` 从契约 app 段取值，bootstrap.go:648–651） |
| `ConfigNamespace` | appkit.go:266 | 条件必需（New 路径可回退 `Name`，appkit.go:391–393） |
| `Registrar` | appkit.go:254 | 条件必需（服务注册） |
| `ConfigFile` / `Env` / `WatchConfigFile` / `RemoteConfig` / `ConfigLayers` / `OnConfigChange` / `StrictContract` | 276 / 306 / 309 / 287 / 300 / 326 / 316 | 条件必需（对应配置能力） |
| `Servers` | appkit.go:330 | 条件必需（手工装配） |
| `Auditor` | appkit.go:337 | **泄漏**：appkit 自身 mount/reconcile 审计事件的 sink 注入（mount.go:166–171），业务不该关心 |
| `StopTimeout` | appkit.go:339 | 条件必需 |
| `BeforeStart`/`AfterStart`/`BeforeStop`/`AfterStop` | 342/345/348/351 | 条件必需（生命周期钩子） |
| `Bind` | appkit.go:224 | 条件必需（业务配置 flag 绑定）；**泄漏**：必须懂“flag 必须进 AppKit 的 FlagSet 才压得过 env/文件”这条装载器内部规则（注释 191–223） |
| `Effect` / `EffectTimeout` | effect.go:48 / 55 | **泄漏**：T1 效应账本，“全局写入配套逆操作”（effect.go:9–11） |
| `Components` / `ComponentTimeout` | component.go:63 / 68 | **泄漏**：C1 组件生命周期 |
| `Provides` / `Requires` | capability.go:34 / 41 | **泄漏**：S1 能力声明符号（capability.go:15–16 自陈“纯声明符号，不做实例管理”） |
| `OnKeyChange` | keywatch.go:44 | **泄漏**：R1 key 级订阅 |
| `Reconcile` | reconcile.go:167 | **泄漏**：R1-2 期望态协调 |

### 2.5 `AppKit` 导出方法（24）

| 符号 | 证据 | 分类 |
|---|---|---|
| `Run` | appkit.go:477 | 必需 |
| `Done` / `Err` | appkit.go:445 / 462 | 条件必需（嵌入式/测试观察） |
| `Settings` | appkit.go:450 | 条件必需（读配置快照） |
| `Config` | appkit.go:459 | **泄漏**：返回装载器具体类型 `*config.Store`（`bootstrap/config` 子包），把配置仓库实现泄露进 App 面 |
| `SetRegistrar` | appkit.go:273 | **泄漏**：运行期改写注册中心，注释自陈只服务 `New` 路径的时序补丁（268–272） |
| `Resolve` | capability.go:52 | **泄漏**：S1 校验，`Run` 已自动调用（注释 50–51 承认“手动调用用于测试”） |
| `ReconcileNow` | reconcile.go:175 | **泄漏**：手动触发协调，注释自陈“供测试与手动触发使用” |
| `UndoEffects` | effect.go:77 | **泄漏**：手动回放账本，注释自陈“主要供 e2e 测试隔离使用”（71–74） |
| `MountComponent` / `UnmountComponent` / `ListComponents` | mount.go:94 / 123 / 153 | **泄漏**：A1 运行期可逆挂载（管理面热插拔） |
| `Ai`/`Ais` | ai.go:66/72 | 条件必需（ai 段）；**泄漏**：返回 `any`，类型安全靠消费侧断言 |
| `Broker`/`Brokers` | broker.go:65/71 | 同上 |
| `Cache`/`Caches` | cache.go:62/68 | 同上 |
| `Database`/`Databases` | database.go:66/72 | 同上，注释明写“类型安全在消费侧恢复：`kit.Database(gormcontract.Type).(*gormcrud.Client)`”（database.go:62） |
| `Storage`/`Storages` | storage.go:63/70 | 同上 |
| `Workflow`/`Workflows` | workflow.go:62/68 | 同上 |

### 2.6 包级函数 / 工具 / 注册表方法（其余）

| 符号 | 证据 | 分类 |
|---|---|---|
| `ComponentFunc` | component.go:44 | 条件必需（把清理函数适配成组件） |
| `DiffStrings` | reconcile.go:300 | 条件必需（协调 diff）；**泄漏**：把集合工具导出到 App 面 |
| `ParseStringList` | reconcile.go:326 | 条件必需；**泄漏**：配置值解析工具（`string`/`[]string`/`[]any`/逗号分隔），属配置装载层职责（reconcile.go:94–132 同类：`settingAtPath`/`settingString` 是同族的点路径读取+标量字符串化，未导出） |
| `WithProbes` | health.go:63 | 条件必需（手工装配探针） |
| `StartMetricsServer` | observability.go:202 | 条件必需（metrics 暴露端）；**泄漏**：App 层直接起 `http.Server` + `ListenAndServe` goroutine（212–216），是能力构造不是编排 |
| `AuditRegistry.Register/MustRegister/Build` | audit_registry.go:92/112/129 | 条件必需 |
| `TracerRegistry.Register/MustRegister/Build` | observability.go:74/94/102 | 条件必需 |
| `MetricsRegistry.Register/MustRegister/Build` | observability.go:143/163/171 | 条件必需 |
| `Registry[T].Register/MustRegister/Get/List` | registry.go:29/40/47/55 | **泄漏**（无框架消费者） |
| `Registry[T].Mount/Unmount` | mount.go:55/71 | **泄漏**（同上） |
| `ReconcileCtx.String/StringSlice/Mounted/Has/Mount/Unmount` | reconcile.go:75/85/136/141/148/153 | 条件必需（R1-2）；**泄漏**：期望态/实际态模型 |

### 2.7 双拼清单（同一机制两个入口各一份导出名）

`Bind`↔`WithBind`、`Registrar`↔`WithRegistrar`、`ConfigFile`↔`WithConfigFile`、
`RemoteConfig`↔`WithRemoteConfig`、`WatchConfigFile`↔`WithWatchConfig`、
`StrictContract`↔`WithStrictContract`、`Provides`↔`WithProvides`、`Requires`↔`WithRequires`、
`Components`↔`WithComponents`、`Effect`↔`WithEffect`、`OnKeyChange`↔`WithOnKeyChange`、
`Reconcile`↔`WithReconcile`、`BeforeStart`↔`WithBeforeStart`、`BeforeStop`↔`WithBeforeStop`
（行号见 2.3/2.4）。**14 组**，共 28 个导出名承载 14 个语义。

导出符号中泄漏类合计：类型 3（`Registry[T]`/`ReconcileCtx`/`PlainBinder`）+ 构造 3
（`NewHarness`/`NewReconcileCtx`/`NewRegistry`）+ Option 17 + 方法 15 + 工具 3 ≈ **41/150**。

---

## 3. 注册表与工厂清单（用户必须显式构造 + 注册）

### 3.1 13 个资源注册表

统一形状：`type XProvider func(...)` + `type XRegistry struct{ mu; providers map[string]XProvider }`
+ `NewXRegistry` + `Register` + `MustRegister` + `Build`。用户最小成本 = **3 行 / 注册表**
（`r := bootstrap.NewXRegistry()` + `r.MustRegister(typ, p)` + `appkit.WithXRegistry(r)`）。

| # | 名称 | 定义 / 构造 `file:line` | 注册内容由谁提供 | 用户必填项 | 证据 |
|---|---|---|---|---|---|
| 1 | `bootstrap.ConfigRegistry` | configregistry.go:56 / :63 | **框架内置 9 个** provider：file/env/http/etcd/consul/nacos/apollo/kubernetes/vault | 至少要注册 1 个源（否则契约 Config 段无法装配） | bootstrap/{file.go:20,env.go:23,http.go:21,etcd.go:19,consul.go:19,nacos.go:21,apollo.go:21,kubernetes.go:21,vault.go:22} |
| 2 | `bootstrap.LogRegistry` | log.go:29 / :35 | 框架内置仅 `BslogLoggerProvider`（log.go:170）；loki/阿里云/腾讯/sentry 等由 `log/<backend>/contract` 提供 | 不接则默认路径只认 `slog` 项，其余 fail-fast（bootstrap.go:1066–1070） | — |
| 3 | `bootstrap.RegistrarRegistry` | registrar.go:55 / :61 | 全部由 `registry/<backend>/contract` 提供 | 用契约 registry 段则必填 | registrar.go:111 fail-fast |
| 4 | `bootstrap.ServerRegistry` | server.go:38 / :44 | 框架**自动补注册** http/grpc（bootstrap.go:568–588，`Has` 判重）；其余（asynq/cron/gateway…20 段）须用户注册 | 仅当用非 http/grpc 协议 | server.go:100–141（32 段只有 2 段 implemented） |
| 5 | `bootstrap.DatabaseRegistry` | database.go:32 / :38 | 全部由 `contrib/database/<engine>/contract` 提供 | 用 database 段则必填 | database.go:124 fail-fast；appkit 侧 database.go:48–49 |
| 6 | `bootstrap.CacheRegistry` | cache.go:31 / :37 | `cache/<backend>/contract` | 用 cache 段则必填 | cache.go:115；appkit 侧 cache.go:46–47 |
| 7 | `bootstrap.StorageRegistry` | storage.go:25 / :31 | `oss/<backend>/contract` | 用 storage 段则必填 | storage.go:109；appkit 侧 storage.go:47–48 |
| 8 | `bootstrap.AiRegistry` | ai.go:29 / :35 | `ai/<backend>/contract` | 用 ai 段则必填 | ai.go:114；appkit 侧 ai.go:50–51 |
| 9 | `bootstrap.WorkflowRegistry` | workflow.go:25 / :31 | `workflow/<engine>/contract` | 用 workflow 段则必填 | workflow.go:131；appkit 侧 workflow.go:46–47 |
| 10 | `bootstrap.BrokerRegistry` | broker.go:26 / :32 | `broker/<impl>/contract` | 用 broker 段则必填 | broker.go:131；appkit 侧 broker.go:49–50 |
| 11 | `appkit.TracerRegistry` | observability.go:63 / :69 | `observability-otlp/contract` | 用 tracer 段则必填 | observability.go:242–243 |
| 12 | `appkit.MetricsRegistry` | observability.go:132 / :138 | 同上 | 用 metrics 段则必填 | observability.go:253–254 |
| 13 | `appkit.AuditRegistry` | audit_registry.go:81 / :87 | `log` 内置（audit_registry.go:160–161）；store/stream 由 `contrib/audit-{store,stream}/contract` 提供 | 用 audit 段且含非 log 项则必填 | audit_registry.go:164–168 |
| + | `appkit.Registry[T]` | registry.go:18 / :24 | —（无框架消费者） | 无 | §2.1；**全仓零引用**：对 `pkg/**`、`internal/**`、`cmd/**`、`_example/**` 搜 `appkit.Registry` / `appkit.NewRegistry` 无任何命中（含测试） |

### 3.2 非注册表但同性质的「用户必须构造并接线」的工厂

| 名称 | 证据 | 说明 |
|---|---|---|
| `transport/grpc.NewGRPCServerWithRegister` | transport/grpc/grpc_server.go:51 | 手工装配路径（`New`）下业务自建 server 实例 |
| `transport/http.NewHTTPServer` | transport/http/http_server.go:37 | 同上 |
| `transport/gateway.NewGatewayServer` | transport/gateway/gateway_server.go:54 | 同上；`FromBootstrap` 路径经 provider 分支调用（bootstrap/server.go:379） |
| `transport/registry` 之外的 15 个 `NewServer` | asynq:113、cron:67、graphql:61、http3:42、kafka:33、mcp:62、rabbitmq:33、redis:34、rocketmq:35、sse:100、tcp:80、webrtc:74、websocket:103 | 各自子 module，走 `WithServerRegistry` 或 `WithExtraServers` |
| `appkit.Bind` / `WithBind` | appkit.go:224 / bootstrap.go:450 | 框架内化 3 个绑定（`--log.*` bootstrap.go:656、`server.http` :805、`server.grpc` :808）；**业务自持配置段必须再写一个** `WithBind(prefix, opt)` |
| `registry.Registrar` 注入 | registry/registry.go:61；appkit.go:254 / bootstrap.go:205 | 单实例注入，与 #3 二选一 |
| `appkit.Component`（实现） | component.go:31 | C1 组件须业务实现 `Name/Start/Dispose` 三方法（`ComponentFunc` 只覆盖“无启动逻辑”的一半，component.go:42–46） |

### 3.3 这组清单对判据 2 的直接含义

- **同一形状被抄了 13 遍**：13 个注册表各自手写 `mu + providers map + Register/MustRegister/Build`
  （如 registrar.go:55–113 与 audit_registry.go:81–199 结构逐行同构）。`appkit.Registry[T]`
  （registry.go:18）本可承载，却无人使用 → 每新增一个资源域 = 重写一遍 ~60–80 行。
- **每新增一个资源域，框架内需同步改动 7 处**（以 database 为例）：
  registry 文件（bootstrap/database.go:32）、`bootstrapSpec` 字段（bootstrap.go:104）、
  `With*Registry`（database.go:34）、`build*`（database.go:43）、阶段 B 调用（bootstrap.go:675）、
  `Effect` 注册（bootstrap.go:767）、AppKit 字段+访问器对（appkit.go:77 / database.go:66,72）。
  ≈ **+200 行框架内代码 + 3 行用户代码 / 资源域**。
- **用户侧装配行数随“用到的能力域”线性增长**：13 个域全用 ≈ 39 行注册 + 若干 `app.Database(...)`
  断言；文档自述 `FromBootstrap` “用户侧样板 ~30-60 行”
  （`docs/devel/zh-CN/AppKit FromBootstrap 约定装配.md:40`），但该数字对应“只用 http/grpc”的最小形态。

---

## 4. 职责边界：appkit 里混入了什么

| 文件 | 行数 | 实际承担的职责 | 是编排还是能力实现 |
|---|---|---|---|
| appkit.go | 772 | App 生命周期编排（Run 143 行、stopAll 36 行、register/deregister、endpoint 就绪轮询、实例构造）+ 配置装载（`loadConfig` 400–442）+ Option 定义 | **编排为主**，但 `loadConfig` 构造 `pflag.FlagSet` 并调 `config.Load`（401–435）是装载器职责 |
| bootstrap.go | 1101 | 契约驱动装配：能力声明→provider 注入→契约段→实例（`FromBootstrap` 454 行 + 日志两阶段工厂 `resolveLoggerFactory`/`defaultLoggerFactory` 1020–1088） | **混淆**：既是编排（12 个 Effect 的注册序），又是能力实现（默认日志后端分派 4 个分支，1047–1088） |
| observability.go | 268 | `TracerRegistry`/`MetricsRegistry` 两个注册表 + `buildObservability` + **`StartMetricsServer`（真起 http.Server，202–218）** | **能力构造**混进 App 层 |
| audit_registry.go | 243 | `AuditRegistry` + `Build`（多后端组装 `MultiAuditor`、失败逆序回滚，148–197）+ `buildAudit`（全局 `SetAuditor`，240–241）+ 后端类型常量 50–59 | **能力构造 + 全局状态改写** |
| reconcile.go | 367 | R1-2 协调器（机制）+ 配置点路径读取与标量解析（`settingAtPath` 94–114 / `settingString` 118–132）+ `ParseStringList`（326–361）+ `DiffStrings` | **混杂**：机制 + 配置工具 + 集合工具 |
| mount.go | 194 | A1 运行期挂载 + **审计事件（`auditComponent` 176–193，读 `authn.AuthClaimsFromContext`）** | 机制 + 业务身份概念 |
| component.go | 116 | C1 组件机制 | 机制 |
| effect.go | 92 | T1 效应账本 | 机制 |
| keywatch.go | 91 | R1 key 级订阅 | 机制 |
| capability.go | 95 | S1 能力声明校验 | 机制 |
| health.go | 99 | 探针 mux 包装（75–83）+ gateway 探针包装（87–98） | **能力构造**（HTTP 路由装配）混进 App 层 |
| registry.go | 64 | 泛型注册表（无消费者） | 死机器 |
| harness.go | 19 | 测试装置（注释自陈生产勿用，15–18） | 测试代码进生产包 |
| doc.go | 65 | 纯注释 | — |
| types.go | 9 | 2 个超时常量 | — |
| ai/broker/cache/database/storage/workflow | 78/77/74/78/76/74 = **457** | 同一段胶水抄 6 遍：`With*Registry` + `build*(registry.Build)` + `X()/Xs()` 访问器 | **纯转发**（无编排逻辑；唯一决策是 `Effect` 注册位置） |

机制模块合计 95+116+92+91+194+367 = **955 行 = appkit 非测试代码的 23.6%**；
6 个域胶水 457 行 = **11.3%**；两者相加 **34.9%** 的 appkit 不服务“App 编排”。

跨层 import（App 层认识业务/框架内部概念）：
- `pkg/audit`：appkit.go:42、mount.go:34、reconcile.go:47、audit_registry.go:46
- `pkg/authn`（身份声明）：mount.go:35、reconcile.go:48；使用点 mount.go:183–185、reconcile.go:278–280
- `bootstrap/config`（具体配置装载器）：appkit.go:40（`Config()` 直接把它暴露为返回值，appkit.go:459）
- `log/bslog`（具体日志后端）：bootstrap.go:52、bootstrap.go:1047–1088
- `health`（具体健康检查）：health.go:10、appkit.go 无——但 `WithProbes` 直接 `mux.Handle` 探针路由（health.go:76–82）

---

## 5. 归因分层

### (i) 框架模块设计导致

1. **注册表抽象没被抽出**：13 个注册表手写同一形状，`Registry[T]` 存在却**全仓零引用**
   （registry.go:18 vs bootstrap/{ai:29,broker:26,cache:31,database:32,storage:25,workflow:25,registrar:55,log:29,configregistry:56,server:38}.go、
   appkit/{observability:63,132,audit_registry:81}；零引用经 `appkit.Registry` / `appkit.NewRegistry`
   全仓检索确认，含测试与示例）。
   影响判据 2。（`registry.go:1–8` 的包注释把它解释为“显式装配哲学”，但哲学不要求 13 份拷贝。）
2. **注册表归位规则事后化**：`observability.go:9–12` 自陈“留在 appkit 因为 TracerProvider/MeterProvider
   构造依赖 otel 包（主模块依赖）”，但 appkit **全包零 otel import**（对 `pkg/appkit/*.go` 搜
   `opentelemetry` 无结果），TracerProvider 只是用户注入的函数类型（observability.go:59）。
   即“10 个域在 bootstrap / 3 个域在 appkit”的分界理由是错的。
   影响判据 1、2（用户得知道“注册表在哪个 module”）。
3. **`any` 边界的类型安全转嫁**：6 个域 ×2 个访问器（`Ai/Ais`…`Workflow/Workflows`，见 §2.5）
   返回 `any`，`app.Database(gormcontract.Type).(*gormcrud.Client)` 靠断言（database.go:62）。
   影响判据 3（业务得 import 具体 contrib 包做断言）。
4. **两个装配入口 + 14 组同义双拼**（§2.7）：同一个机制在 `Option` 与 `BootstrapOption` 各导出一次，
   用户必须知道“我在哪条路径上”。影响判据 4。
5. **框架内部时序约束外泄为 3 个逃生舱**：`WithGRPCOptions`（bootstrap.go:188–201）、
   `WithExtraServerFunc`（229–241）、`WithExtraServers`（225）——注释自陈是为绕开
   “构造期拿不到运行期资源”，而同一文件 533–543 又说明该根因已通过“服务器构造延后到 Run 期”根治
   （并有测试 `wiring_order_test.go:27–65` 钉死 `beforeStart → register` 顺序）。
   根因修了、逃生舱留着。影响判据 4（学习面）。
6. **不存在“业务域”这个一等概念**：appkit 的可扩展轴是“资源域/机制”（Server/Component/Registry/
   Effect/Reconcile），全包无按业务域注册的 API。证据：`internal/codegen/appspec_gen.go:59–69` 的
   AppSpec 只有 `meta/server/components/capability/audit_backends/bundle_normalized`，无域字段；
   生成物把业务入口留成两个 TODO 桩（HTTP `// TODO: 注册业务路由` appspec_gen.go:109、
   gRPC `func registerGRPCService(s *grpc.Server) {}` appspec_gen.go:214）。
   **这是判据 1 的核心**：新增一个业务域必须改装配文件（`_example/bald/main.go:118`
   `root.AddCommand(usercmd.NewUserCommand())` 即一例），因为框架没有“域”可注册。
7. **`WithConfigNamespace` 必填**（bootstrap.go:640–645）：框架装载器“env 前缀由配置命名空间派生”
   的设计约束（appkit.go:56–66 详解自指），把 `BaldConfigNamespace != app.name` 这个内部区分
   变成每个用户的必答题。影响判据 4。

### (ii) 历史或示例用法导致

1. **示例 main.go 体量**：`_example/bald/main.go` 441 行，装配段收敛在 `newApp`（177–249），
   其余是路由/handler/教学注释。文档记录“~560 → ~340 行”的演进
   （AppKit FromBootstrap 约定装配.md:259）——说明行数问题里有一部分已修，剩下一部分是
   教学注释而非装配。
2. **生成器抄的是单个真实项目的形状**：`internal/codegen/app.go:17` 自陈素材来自
   “kalandramo/bald-admin M10 落地后的真实装配形状”；`appspec_gen.go:19–24` 的“装配纪律”也是
   从该项目审查中固化。模板里 `buildComponent` 的 `switch kind`（appspec_gen.go:198–209）
   只实现 `heartbeat` 一种，其余“TODO: 接入真实组件工厂”——这是历史项目残留而非框架设计。
3. **跨 module 测试逼出的导出装置**：`harness.go:3–6` 自陈“examples/*、tests/* 是独立 Go module，
   无法用包内 export_test.go”，故导出 `NewHarness`。仓库 93 个 `go.mod`（`Get-ChildItem -Recurse -Filter go.mod` 计数 = 93）是根因。
4. **`_example/bald/user` 域 5 个文件**（cmd/entity/util/provider_mem/provider_gorm），
   已超过判据 1 的“3 个文件”；但这属示例呈现方式，不是 appkit 的设计约束。

### (iii) 证据不足 / 无法归因

1. **“30 分钟可跑起来”（判据 4）无任何可测证据**：仓库无计时数据；唯一量化是文档的
   “用户侧样板 ~30-60 行”（AppKit FromBootstrap 约定装配.md:40）。
2. **12 个 Effect 的停机逆序是否真的必需**：顺序在注释中大量声明（bootstrap.go:596–631、
   719–826），但只有 2 个不变量被测试钉死（`wiring_order_test.go`、`audit_wiring_order_test.go`），
   完整 12 段回放序无断言 → 无法判断复杂度是“确定的依赖对称拆除”还是“经验性叠加”。
3. **判据 3 的口径未定**：`pkg/store`、`bconf/gen/go/...` 算不算“框架内部包”？
   业务域 `_example/bald/user/entity.go` 明确 import 了 `bconf/gen/go/bald/store/v1` 与 `pkg/store`
   来构造 `storev1.PagingRequest`/`storev1.FilterExpr`；若这些都算，判据 3 在业务代码层面已不成立。
4. **`registry.go` 的泛型注册表是否有外部消费者**：本仓零引用（含测试/示例/cmd，
   经 `appkit.Registry`/`appkit.NewRegistry` 全仓检索确认），但它是导出 API，
   可能服务仓库外集成方 → 待考。
5. **`FromBootstrap` 454 行单函数**是否可拆，取决于作者是否要求“单文件可读”这一目标 → 取舍，非缺陷。

---

## 6. 有意取舍（明确不计为缺陷）

- **stopAll 五阶段 + 未取消 ctx 传 Stop**（appkit.go:621–666）：`Stop` 传 `context.Background()`
  而非已取消的 ctx 是 go-lulu 的既有防坑（BUG-1，doc.go:35），代价小、收益明确。
- **能力声明 S1 fail-fast**（capability.go:45–94）：把 `nil` panic 提前为启动期错误，
  是纯收益，95 行成本可接受。
- **显式注册表 + 不用 `init()` 自注册**（registrar.go:13–15、registry.go:6–8）：
  依赖图可见、未 import 的后端零编译成本，是明确的取舍；缺陷不在“显式”，而在“同一个显式形状抄 13 遍”。
- **两条装配入口并存**（FromBootstrap / New）：文档给了判据（AppKit FromBootstrap 约定装配.md:32–48），
  e2e 复用 `newApp` 也依赖它；代价是 14 组双拼导出名（§2.7），这是取舍而非错误。
- **`Ais()`/`Brokers()` 等返回副本**（ai.go:72–77）：防外部改写内部 map，正确。
- **审计旁路不阻断**（mount.go:176–193、reconcile.go:271–294）：有 `recover` + 仅记日志，
  与“审计不能拖垮业务”纪律一致。

---

## 7. 四条硬判据的当前证据状态

| 判据 | 证据 | 状态 |
|---|---|---|
| 1. 新增业务域只动 3 文件、零装配修改 | appkit 无“域”概念（appspec_gen.go:59–69 无域字段）；生成物业务入口是 TODO 桩（appspec_gen.go:109、214）；示例域需改 `_example/bald/main.go:118` | **不成立**（缺一等概念，非实现瑕疵） |
| 2. 装配代码行数不随业务域增长 | 装配行数随**资源域/机制**增长：13 注册表（§3.1）、每域 7 处改动（§3.3）、6 域胶水 457 行、机制 955 行（§4） | **按“业务域”口径或成立，按“能力域”口径不成立**；当前框架的扩展轴是能力域 |
| 3. 业务代码零 import 框架内部包 | `_example/bald/user/entity.go` import `bconf/gen/go/bald/store/v1` + `pkg/store`；`provider_mem.go` import `pkg/store/inmemory`；生成物 main.go import 10 个框架包（appspec_gen.go:55–69） | **口径未定**；若含 pkg/* 与生成契约则**不成立** |
| 4. 新服务 30 分钟可跑 | 无计时证据；文档“30-60 行样板”（AppKit FromBootstrap 约定装配.md:40）；必答项：`WithConfigNamespace`（bootstrap.go:640） | **待考** |

---

## 8. 待作者确认

1. 「业务代码」是否含 `main.go`/装配文件与 `pkg/*` 公共包？决定判据 3 成不成立。
2. 「业务域」的框架侧定义：是否接受「域 = 一组 handler + 一组 service 注册」，还是希望框架提供
   域注册原语（如 `appkit.Domain(name, registrar)`）？
3. `appkit.Registry[T]` 是否服务仓库外消费者？若无，建议降为未导出或删除（它当前零消费者）。
4. 12 段 Effect 停机逆序是否有未落库的设计依据？决定 §4 的复杂度是“结构性”还是“累积性”。
5. `observability.go:9–12` 的“依赖 otel 所以留 appkit”与实测零 otel import 矛盾——是历史原因
   （曾被主模块依赖）还是笔误？
