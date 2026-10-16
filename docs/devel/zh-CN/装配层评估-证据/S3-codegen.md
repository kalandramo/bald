# S3 · bald 代码生成器审计（只读证据）

范围：`D:\code\konglingfei\bald`（module `github.com/kalandramo/bald`）+ 参考示例 `D:\code\konglingfei\bald-admin`。
约束：未修改任何仓库文件；未运行 `go build`/`go test`/git 写操作。
说明：任务书给出的行号/文件名与工作树现状有漂移，本文以实测为准并在 §6 列出。

---

## 0. 前置更正（任务书前提的两处偏差）

| 任务书说法 | 实测 | 证据 |
|---|---|---|
| 四个子命令：gen proto / gen store / gen app / **gen appspec** | 只有 **3 个**子命令；`appspec` 不是独立子命令，而是 `gen app` 的 `--spec` 模式 | `internal/codegen/gen.go:82` `cmd.AddCommand(genProtoCmd(streams), genStoreCmd(streams), genAppCmd(streams))`；`app.go:155-158` `if o.Spec != "" { return runGenAppSpec(...) }`；`app.go:221` flag 定义。全仓 grep `appspecCmd` = 0 命中 |
| `app.go` 184 行 / `appspec_gen.go` 281 行 | `app.go` **250** 行；`appspec_gen.go` **368** 行 | `read` 工具 total 行数；`app.go:180` 是 `genAppCmd`，`gen.go:198` 是 `genStoreCmd` |
| `bald-admin/cmd/bald-admin/app/audit.go` | 实际路径 `bald-admin\backend\cmd\bald-admin\app\audit.go` | 目录不存在 `bald-admin\cmd\`（Get-ChildItem 报 PathNotFound） |

`internal/codegen` 全部文件（6 个）：
`gen.go`(240) · `app.go`(250) · `appspec_gen.go`(368) · `iostreams.go`(54) · `app_test.go`(263) · `guard_test.go`(148)。
`cmd/bald` 全部文件：仅 `main.go`(41)。

---

## 1. 四（实为三）子命令现状测绘

### 1.1 `bald gen proto <name>`

- 入口：`gen.go:131-153`（`genProtoCmd`），Options 三阶段 `gen.go:98-129`。
- flag：`--out`（`gen.go:148`）、`--go-package`（`gen.go:149-150`）、`--force`（`gen.go:151`）；位置参数 `Args: cobra.ExactArgs(1)`（`gen.go:136`）。
- 默认输出：`filepath.Join("api","proto","bald",<name>,"v1")` → `api/proto/bald/<name>/v1/<name>.proto`（`gen.go:102-104`，落盘 `gen.go:122`）。
- 产物：**1 个文件**；模板 `protoTmpl` 位于 `gen.go:24-58`（35 个源行）。
- 输出结构（逐行推导）：31 行（无 `--go-package`）/ 32 行（有）。内容 = `package bald.<name>.v1` + 2 个 import + `option go_package`（条件）+ `service <Name>Service{List,Get}` + 4 个 message（`<Name>` / `ListRequest`(带 `bald.store.v1.PagingRequest`) / `ListResponse`(带 `PaginationResponseMeta`) / `GetRequest`）。**0 个 Go 函数**。
- 模板内的条件块：`gen.go:31-33`（go_package）。
- 装配段落占比：**0%**。产物是契约文本，不含任何装配。
- 命令尾部只打印提示，不做 buf 调用（`gen.go:125-128`）。

### 1.2 `bald gen store <name>`

- 入口：`gen.go:198-219`；Options `gen.go:165-196`。
- flag：`--out`（默认 `.`，`gen.go:169-171`）、`--out-pkg`（默认 `main`，`gen.go:172-174`）、`--force`（`gen.go:217`）。
- 产物：**1 个文件** `<out>/<name>.go`（`gen.go:195`）；模板 `entityTmpl` 位于 `gen.go:61-71`（11 个源行）。
- 输出结构：**10 行** = package 声明 + 1 个 struct（2 字段：`ID string gorm:"primaryKey" json:"id"`、`Name string json:"name"`）+ **1 个函数** `KeyOf<Name>(u *<Name>) string { return u.ID }`。渲染后强制 `format.Source`（`gen.go:191-194`）。
- 装配段落占比：**0%**。且默认 `package main` + 默认写当前目录（`gen.go:170-174`），**不含任何分层目录约定**（不写 `internal/...`）。
- 测试覆盖：**0**。`genStoreCmd`/`storeOptions` 全仓仅出现在 `gen.go` 自身（`gen.go:82,156,165,178,186,198,199`），`app_test.go`/`guard_test.go` 均未触及。

### 1.3 `bald gen app <name>`（模板模式）

- 入口：`gen.go:82` 挂载 → `app.go:180-224`（`genAppCmd`）；`Run` 在 `app.go:155-172`。
- flag：`--out`（默认 `cmd/<name>/main.go`，`app.go:219` + `app.go:139-141`）、`--module`（**仅提示用，`Complete` 里被丢弃**：`app.go:142` `_ = o.Module`）、`--spec`、`--force`（`app.go:220-222`）。
- 产物：**1 个文件**；模板 `appTmpl` 位于 `app.go:24-120`（源行 24–119 共 **96 行**，无任何条件块 → 输出 = 96 行，gofmt 仅做对齐）。
- 函数清单（4 个）：`main`(`app.go:44-49`)、`newApp`(`app.go:51-98`)、`buildBundle`(`app.go:103-111`)、`buildServers`(`app.go:114-119`)。
- 装配段落占比：**100%**。文件内唯一的非代码元素是 4 处 `[FILL]` 注释（`app.go:83,92,95,118`）。
- **死代码**：`buildServers` 在生成物中从未被调用；`newApp` 里 servers 只有注释行 `// appkit.Servers(httpSrv, grpcSrv)`（`app.go:95-96`），`buildBundle` 仅被 `buildServers` 调用（`app.go:115`）→ 生成的 4 个函数里有 2 个（`buildBundle`/`buildServers`）在 main 路径上不可达，业务必须自己把它们接到 `appkit.Servers`。
- 生成物**无目录结构**：单文件写在 `cmd/<name>/main.go`，无 `internal/`、`biz/`、`pkg/`。
- 生成后只打印 `next: fill [FILL] points, ...`（`app.go:170`）。

### 1.4 `bald gen app --spec <file>`（AppSpec 方言模式）

- 入口：`app.go:227-249`（`runGenAppSpec`）→ `appspec_gen.go:351-368`（`renderAppSpec`）；模板 `appspecTmpl` 位于 `appspec_gen.go:28-338`（源行 **311 行**）。
- 解析：`os.ReadFile` + `protojson.Unmarshal`（`app.go:228-235`），**未设置 `DiscardUnknown`**（`app.go:233`）→ 未知 JSON 字段报错（protojson 默认严格）；**无任何域校验**（见 §5）。
- 默认输出：`cmd/<AppSpec.meta.name>/main.go`（`app.go:236-240`）。
- 产物：**1 个文件**；行数随条件块变化（无实测样本；全开时估算 ≈300 行，**标待考**）。
- 条件块（模板内 16 处 `{{if}}`）：`appspec_gen.go:47-49`(gin) `50-53`(grpc) `59-70`(http‖grpc 的 4 个 import) `64-66`(grpcserver) `67-69`(httpserver) `75-77`(auditBackends range) `80-88`(bootstrap+addr) `93-103`(bundle) `104-110`(gin router) `122-126`(Bind) `128-133`(OnKeyChange) `134-146`(capability) `147-158`(components) `159-173`(Servers) `184-192`(healthChecker) `211-220`(grpc 两函数)。
- 函数清单（全开时 11 个 + 1 类型 + 3 包级 var）：
  `serveRunE`(79-181) · `ready`(195) · `buildComponent`(198-210) · `registerGRPCService`(214，**空实现**) · `newGRPCServerOptions`(217-219) · `reconcileAudit`(227-253) · `auditBackendComponent.Name`(266) · `.Start`(268-280) · `.Dispose`(282-289) · `buildAuditBackend`(294-305) · `applyAuditors`(309-323) · `main`(325-335)；type `auditBackendComponent`(264)；var `defaultAuditBackends`(75-77) · `healthChecker`(187-191) · `auditors`(257-260) · `osExit`(337)。
- 装配段落占比：**≈100%**（全文件只有 5 处业务占位注释 TDOO：`appspec_gen.go:100,109,200,204,213,298`）。
- **与手写装配同名同形（已核实）**：`reconcileAudit`、`auditBackendComponent`、`buildAuditBackend`、`applyAuditors` 与 `bald-admin\backend\cmd\bald-admin\app\audit.go:32,55,63,77,101,133` 一一对应；模板是手写版**删减后**的副本。
- 模板相比手写版的**退化（不是简化，是丢修复）**：
  1. `Dispose` 不关闭后端（`appspec_gen.go:282-289`）——手写版有 `closer.Close()` 收尾，注释明确写了「否则每次热切换泄漏一个 goroutine + 1024 容量 chan，且已入队事件被静默丢弃」（`bald-admin…app\audit.go:82-90`）。
  2. `reconcileAudit` 用 `rctx.StringSlice`（`appspec_gen.go:228`），手写版用 `parseAuditBackends(rctx.String(...))` 做**值域白名单**（`audit.go:134,178-193`）。
  3. `buildAuditBackend` 只剩 `log` 分支（`appspec_gen.go:294-305`），手写版含 store/stream（`audit.go:101-123`）。
  4. `applyAuditors` 未处理空集合（`appspec_gen.go:309-323`），直接 `audit.NewMultiAuditor(list...)`；此处**恰好安全**（`pkg/audit/multi.go:25-35` 对空列表返回 `NopAuditor()`），模板注释 `appspec_gen.go:308` 的「NewMultiAuditor 兜底」成立。
- **声明字段无消费者**：`ServerSpec.gateway`（`bconf/proto/bald/appspec/v1/appspec.proto:52`）在 `internal/codegen` 中 **0 次引用**（grep `Gateway` 命中数 0）→ 声明了 gateway 也生成不出任何接线，静默无效。
- 落盘异常路径：gofmt 失败时**绕过防覆盖保护**直接 `os.WriteFile`（`appspec_gen.go:357-362`），即在无 `--force` 情况下也可能覆盖目标文件（自注释为「诊断性落盘」）。

### 1.5 三命令共用的落盘与边界

- 落盘唯一通道 `writeFileGuarded`（`iostreams.go:38-54`）：已存在即拒绝、提示 `--force`、`generated: <path>` 走 `Out`。
- `gen proto`/`gen store` 的唯一测试：`guard_test.go:83-122`（proto 二次拒绝 + `--force`）、`guard_test.go:20-79`（写守卫单元）。
- `cmd/bald` 与 `internal/codegen` 边界：**干净**。`cmd/bald/main.go` 41 行，只做 cobra root + `codegen.NewCommand(codegen.NewDefaultIOStreams())`（`main.go:28-41`），无模板/业务逻辑；所有 flag/模板/落盘都在 `internal/codegen`。`codegen` 也未反向依赖 `cmd`。

---

## 2. AppSpec 方言的表达能力

### 2.1 能表达（`bconf/proto/bald/appspec/v1/appspec.proto:15-86`）

| 声明面 | 字段 | 生成物消费点 |
|---|---|---|
| 应用标识 | `meta.name/module/desc`（proto:36-43） | `appspec_gen.go:35,53,115,117,177,327-328` |
| 传输面开关 | `server.http/grpc`（proto:48-49） | `appspec_gen.go:47,50,104,159-172` 的 `{{if}}` |
| 监听地址 | `server.http_addr/grpc_addr`（proto:54-56） | `appspec_gen.go:82-87`（写进 `bconf.NewBootstrap()` 的默认值） |
| 组件（C1） | `components[].kind/name/config_prefix`（proto:60-68） | `appspec_gen.go:147-158` + `buildComponent`(`198-210`) |
| 能力（S1） | `capability.provides[]` / `requires[]{component,caps[]}`（proto:71-86） | `appspec_gen.go:134-146` |
| 审计期望态（R1-2） | `audit_backends[]`（proto:29） | `appspec_gen.go:75-77` + `reconcileAudit`(`227-253`) |
| 归一化链（P9/P10） | `bundle_normalized`（proto:32） | `appspec_gen.go:97-99` |

### 2.2 不能表达（仍必须手写 Go）

- **业务类型引用**：组件工厂（`appspec_gen.go:198-210` 只实现 `heartbeat`，其余 kind 落到 `default:` 运行期错误 `appspec_gen.go:207-209`）、审计 store/stream 后端（`appspec_gen.go:298-302` TODO 明确点名需要 `bootstrappkg.DB / RedisCache.Client()`）、gRPC service 注册（`appspec_gen.go:214` 空函数）、HTTP 业务路由（`appspec_gen.go:109` TODO）、bundle 的 Authn/Authz/Metrics 实例（`appspec_gen.go:100` TODO）、`ready` 就绪判定（`appspec_gen.go:195` 恒 nil）。
- **框架级配置段**：AppSpec 无 `log` / `registry` / `database` / `cache` / `storage` / `tracer` / `metrics` / `server.http.middleware` / `server.asynq|cron|sse` 段。这些在 bconf 侧**都已契约化**（`bconf/proto/bootstrap/v1/{log,registry,database,cache,storage,tracer,metrics,server}.proto`），而在 bald-admin 里对应 6 个 Registry + `bundle.FromMiddleware`（`assembly.go:150-161,352-364,373-377,608`）——AppSpec 一个字都表达不了。
- **表达不了的还有装配时序/降级策略**：bald-admin 的 `WithBeforeStart` 159 行（`assembly.go:403-561`）承载「TokenStore 必须先于路由注册否则吊销静默失效」「无 Redis 时 fail-open/fail-closed 分叉」等语义，属于代码而非声明。
- `meta.module` 声明了但**不落地**：`app.go:142` `_ = o.Module`，`appspec_gen.go:35` 只写进注释，import 路径硬编码 `github.com/kalandramo/bald/...`（`appspec_gen.go:55-69`）→ 生成物只适用于用 bald 核心模块路径的消费者。

### 2.3 耦合/膨胀风险（会线性膨胀）

1. **每个新能力 = 3 处同步改**：proto 字段 + `appspecData`（`appspec_gen.go:342-349`，纯手工转写）+ 模板条件块 + 测试锚点（`app_test.go:111-120`）。`appspecData` 现有 6 个字段，全是逐字段手写赋值（`app.go:241-248`）——没有泛化/遍历，**字段数 N 与代码量线性**。
2. **组件 kind 是字符串开关**：`buildComponent`（`appspec_gen.go:198-210`）是 `switch kind`，proto 注释里的取值清单（`appspec.proto:62`）是**唯一的"枚举"**，加一个 kind 就要改模板 switch + 文档 + 白名单，且是运行期才报错的弱校验。
3. **配置键是字符串字面量**：模板把 `"audit.backends"`(`120,228`)、`"server.http"`/`"server.grpc"`(`124-125`)、`"server.http.addr"`(`130`) 写成裸字符串，与 bconf 契约无编译期关联；bconf 提供 `FindUnknownKeys`（`bconf/unmarshal.go:41`）也没被生成物或用例使用。
4. **双真相源并未消除，只是换了位置**：AppSpec 是**生成期**真相源，运行期仍读 config（`appspec_gen.go:73-74` 注释与 `appspec_gen.go:228-230` 的 `want→defaultAuditBackends` 回退）。同一件事（审计后端集合）在 AppSpec、config `audit.backends`、`defaultAuditBackends` 变量三处存在；改 AppSpec 若忘记重新生成，则运行期用旧声明（且无检测）。

---

## 3. 可声明化候选清单

判定口径：**纯样板** = 不含业务类型引用、不含业务策略分支，可由声明或框架内建取代；**必须保留** = 引用业务类型（pb/biz/连接实例）或承载业务时序/降级决策。

### 3.1 来自 `internal/codegen/appspec_gen.go`（生成物）

| 段落 | 文件:行 | 判定 | 取代它的声明示例 |
|---|---|---|---|
| `defaultAuditBackends` + `serveRunE` 的 bootstrap/addr 覆盖 | `appspec_gen.go:75-77,80-88` | 纯样板（已声明驱动） | 已是 `audit_backends` / `server.http_addr`；无需再改 |
| 日志装配 `logOpts` + `SetLogger` | `appspec_gen.go:90-92` | 纯样板——**可从 bconf `log` 段直出，而不是硬编码 `bslog.NewOptions()`** | `log: {level: info, format: json, rotate: {max_size_mb: 100}}` |
| `bundle.New` + `Normalized()` | `appspec_gen.go:95-102` | 半样板：`Normalized` 已声明；Authn/Authz/Metrics 注入需业务实例 → **保留生成 + 补声明字段** | `bundle: {normalized: true, layers: [metrics, audit]}`（`metrics.recorder` 仍由业务注入） |
| gin router + `b.Gin()` | `appspec_gen.go:106-108` | 纯样板 | 无需声明（http=true 即隐含） |
| `capOpts` 列表 + `Bind`×3 | `appspec_gen.go:114-127` | 纯样板——**应由框架按契约段自动 Bind（`FromBootstrap` 已内化，见 `pkg/appkit/bootstrap.go:656,805,808`），不该每次生成** | 无（框架行为） |
| `OnKeyChange` 示例 | `appspec_gen.go:128-133` | 纯样板 | `hotkeys: [server.http.addr]` |
| `Provides/Requires` | `appspec_gen.go:134-146` | 纯样板（已声明驱动） | 已是 `capability.*` |
| 组件装配循环 + `buildComponent` | `appspec_gen.go:147-158,198-210` | **混合**：装配循环纯样板；`kind→工厂` 需业务类型 → 保留 + 声明字段（补 `params`） | `components: [{kind: db, name: app.db, params: {dsn_env: APP_DSN}}]` |
| Servers 装配（健康探针 + http/grpc server 构造） | `appspec_gen.go:159-173` | 纯样板（协议实现固定）——应下沉为框架原语 `appkit.ProbeServers(...)` | `server: {http: true, grpc: true, probes: true}` |
| `healthChecker` / `ready` | `appspec_gen.go:184-195` | 探针装配纯样板；`ready` 业务就绪判定**必须手写** | 探针装配无需声明；`ready` 保留为注入点 |
| `reconcileAudit`（通用 diff-apply 协调器） | `appspec_gen.go:227-253` | **纯样板**——同一实现已被手写复制一份（`bald-admin…audit.go:133-174`）→ 应内建为框架 reconciler，而不是"每个项目生成一份" | `audit: {backends: [log, store], reconcile: auto}` |
| `auditors` 全局表 + `auditBackendComponent` + `applyAuditors` | `appspec_gen.go:257-289,309-323` | 纯样板（生命周期 + 全局 MultiAuditor 重建）→ 框架内建 | 同上 |
| `buildAuditBackend` | `appspec_gen.go:294-305` | **必须保留**（引用业务 DB/Redis 与 contrib 实现） | 声明只能给 kind 列表：`audit: {backends: [log, store, stream]}` |
| `registerGRPCService` / `newGRPCServerOptions` | `appspec_gen.go:213-219` | 必须保留（注册引用业务 pb 类型）——但**可由 proto 反射生成** | 从 proto 契约（`bald-admin/backend/api/protos/<域>/v1/*.proto`）自动列出 service → 生成注册桩 |
| `main` + cobra root | `appspec_gen.go:325-337` | 纯样板——应内建为 `appkit.RunCLI(...)` 或由框架 `cmdkit` 提供 | 无 |

### 3.2 来自 `bald-admin\backend\cmd\bald-admin\app\`（手写装配，非测试文件共 1439 行）

| 段落 | 文件:行 | 判定 | 取代它的声明示例 |
|---|---|---|---|
| `serveRunE` 三阶段 | `assembly.go:63-97` | 纯样板（框架已内化大半） | 无（框架行为） |
| `buildApp` 构造期骨架 + `bundle.FromMiddleware` | `assembly.go:105-171` | 半样板：中间件段已是契约驱动（`bcnf server.http.middleware`）→ 保留但应生成 | `server: {http: {middleware: {cors: {}, request_id: {}}}}` |
| `opts := []appkit.BootstrapOption{...}` 主体（Registry×6、Provides/Requires、OnKeyChange、Reconcile、Effect、AfterStart/BeforeStop） | `assembly.go:309-583` | **纯样板占多数**：`WithDatabaseRegistry`/`WithCacheRegistry`/`WithStorageRegistry`/`WithTracerRegistry`/`WithMetricsRegistry`/`WithRegistrarRegistry`/`WithServerRegistry`/`WithAuditRegistry` 的**选择**由契约 `type` 字段决定，可声明化；provider 代码（`registries.go`）保留 | `database: {type: sql}`, `cache: {type: redis}`, `tracer: {type: otlp}` —— bconf 已有这些段，缺的是"生成注册器"而非声明字段 |
| `WithStrictContract` / `WithBind` / `WithConfigFile` / `WithConfigNamespace` | `assembly.go:337-346,614` | 纯样板 | 无 |
| `WithBeforeStart` 业务桥接 159 行 | `assembly.go:403-561` | **必须保留**（业务时序：TokenStore→认证器→路由；nil 降级策略） | 不可声明（策略语义），最多声明"启用哪些桥接" |
| `registerGRPC`（11 个 service 注册） | `assembly.go:274-288` | 必须保留 → **可生成**（同 3.1 末行） | 由 proto 目录清单驱动 |
| `reconAuditors` + `applyAuditors` | `audit.go:24-49` | 纯样板（与模板重复） | 框架内建 |
| `auditBackendComponent` | `audit.go:55-93` | 纯样板 | 框架内建 |
| `buildAuditBackend` | `audit.go:101-123` | 必须保留（引用 `bootstrappkg.DB/RedisClient`、`securityaudit.New*`） | `audit: {backends: [log, store, stream], stream: {name: audit.events, buffer: 1024}}` |
| `parseAuditBackends` 白名单 | `audit.go:178-193` | 纯样板（后端名域）→ 应变成 proto enum | `enum AuditBackend { LOG; STORE; STREAM; }` |
| `reconcileAudit` | `audit.go:133-174` | 纯样板（与模板重复） | 同上 |
| `newGRPCServerOptions`（bundle 链 + 校验层） | `servers.go:29-60` | 必须保留（authn/authz 实例 + 业务链序） | 可部分声明：`server: {grpc: {interceptors: [validation]}}` |
| `registerGateway`（11 个业务网关注册） | `servers.go:66-103` | 必须保留 → **可生成** | 由 proto 的 `google.api.http` 注解驱动生成 |
| `registries.go`（tracer/metrics/config/registrar/database/cache/server/storage 8 个注册器，240 行） | `registries.go:29-240` | 保留（provider 实现），但**每个注册器的存在本身就是"声明面缺失"的症状** | 契约段已在 bconf，缺的是"按段自动装注册器"的框架能力 |
| `components.go` heartbeat 组件 | `components.go:48-83` | 纯样板（启动/Dispose 打印） | `components: [{kind: heartbeat, name: demo.heartbeat}]` |
| `builders.go`（login limiter/breaker/retrier） | `builders.go:20-123` | 必须保留（业务配置段→框架原语） | `login: {rate_limit: {...}, breaker: {...}, retry: {...}}` + 生成 `builder` 调用 |
| `sse.go` / `asynq.go` / `cron.go` / `scheduler.go` | `sse.go:61-198`, `asynq.go:58-71`, `cron.go:41`, `scheduler.go:34-63` | 必须保留（引用业务类型/回调） | `server: {asynq: {...}, cron: {...}, sse: {...}}`（bconf `server.proto` 已有段） |

**结论**：可声明/可框架内建的样板主要是「配置绑定 + 注册器选择 + 审计协调器 + 组件/探针装配 + 命令壳」；真正必须留在 Go 里的是「业务类型引用（pb service、biz、DB/Redis 实例）+ 业务时序与降级策略」。当前把这两类**混在同一个 300+ 行模板**里，是"生成物像 bald-admin 手写装配"的直接原因。

---

## 4. 目标骨架的缺口分析

目标形态：`gen app` = 声明式装配（main 极薄）+ 简洁架构分层骨架（cmd/internal/biz/pkg 边界正确，业务目录空模板）+ 一条可运行端到端示例链路（proto → biz → handler）。

| # | 缺什么 | 为什么需要 | 证据 |
|---|---|---|---|
| G1 | **声明面不完整**：AppSpec 只有 6 个字段，没有 log/registry/database/cache/storage/tracer/metrics/middleware/server.* 段 | 这些段在 bconf 已全部契约化、在 bald-admin 已全部由 Registry 消费；不声明它们，装配就不可能"main 极薄" | `appspec.proto:15-33`（6 字段）vs `bconf/proto/bootstrap/v1/` 的 16 个 proto（`database/cache/storage/tracer/metrics/log/registry/server/...`） |
| G2 | **生成物用旧装配路径**：模板走 `appkit.New(capOpts...)` + 手写 `bconf.NewBootstrap()` + 手写 `Bind`×3 | bald-admin 自己已迁到 `appkit.FromBootstrap`（把 Bind×3、日志两阶段、registrar、可观测性、停机 Effect 全内化），模板仍生成被替代的写法 → 生成物比参考示例旧一代，且拿不到 `FromBootstrap` 的契约一致性 fail-fast | 模板 `appspec_gen.go:81,114-127,175` vs `bald-admin…assembly.go:229-249,616`；`pkg/appkit/bootstrap.go:460,656,805-808`；校验逻辑 `bootstrap.go:478-498`（`WithHTTP` 声明但契约 `server.http` 为 nil → 报错）在 `New` 路径上**不存在** |
| G3 | **无分层骨架**：`gen app` 只写单文件 `cmd/<name>/main.go` | 目标形态要求 cmd/internal/biz/pkg 边界正确 + 业务目录空模板；单文件无法承载 `internal/apiserver/{biz,handler,model}` 结构 | `app.go:139-141`（默认 `cmd/<name>/main.go`）、`appspec_gen.go:336`（模板 `package main` 单文件）；对照 `bald-admin\backend\internal\apiserver\` 的 biz/handler/model 三层 + 分层不变量测试 `layering_invariant_test.go:34` |
| G4 | **`gen store` 无分层约定** | 默认 `--out-pkg main` + 写 CWD（`gen.go:170-174`），业务实体落到哪里全靠人 | 同左；且无测试（§1.2） |
| G5 | **无端到端示例链路**：`gen proto` 只出契约文本（`gen.go:24-58`），不带 biz/handler/convert/路由注册/HTTP 注解；`registerGRPCService` 是空函数 | 目标形态要求"一条可运行的示例链路供照着复制"；现状要跑通必须回抄 `_example/bald/user/`（手写 82 行 entity + 2 个 provider + cmd.go）或整个 bald-admin | `appspec_gen.go:213-214`、`gen.go:24-58`；手写示例 `_example/bald/user/{entity.go(82),provider_gorm.go(27),provider_mem.go(10),cmd.go(30)}` |
| G6 | **无生成期语义校验**：AppSpec 只做 protojson 反序列化 | 未知 kind 现在生成后才在运行期炸（`unknown component kind`）、audit_backends 值域不校验、`requires⊆provides` 不校验、component name 唯一性不校验 | `app.go:227-235`（只 Unmarshal）；`appspec_gen.go:207-209`（运行期错误）；对照手写版有白名单 `audit.go:178-193` 与 `WithStrictContract`(`assembly.go:342`) |
| G7 | **无生成物 ↔ bconf 契约一致性校验** | 模板把配置键写成字符串（`appspec_gen.go:120,124-125,130`）；bconf 改字段名时生成物静默失效（配置键不存在 → `DiscardUnknown:true` 静默忽略） | `bconf/unmarshal.go:151-153`（`DiscardUnknown: true`）；`bconf/unmarshal.go:41` 提供 `FindUnknownKeys` 但 codegen 与测试均未使用 |
| G8 | **声明字段无消费者**：`server.gateway` | 声明了不生效 = 最坏形态（看起来支持）；且设计文档自己写了"模板 TODO 改为生成该接线"但没做 | `appspec.proto:52` vs `internal/codegen` grep `Gateway` = **0**；文档 `代码生成工具设计.md:91`；框架侧入口已就绪 `pkg/appkit/bootstrap.go:178`（`WithGatewayRegister`） |
| G9 | **无生成物"目录级/结构级"断言** | 现有测试只断言字符串锚点与能否编译，无法阻止"生成物不含分层/示例链路" | `app_test.go:41-49,111-120`（`strings.Contains` 锚点） |
| G10 | **无 gateway/多 server/多 kind 的运行时覆盖** | 冒烟只覆盖无传输 + heartbeat | `app_test.go:177-227`（`Server: &appspecv1.ServerSpec{}`、唯一组件 `heartbeat`） |

### 4.1 现有测试覆盖了什么 / 没覆盖什么

| 测试 | 文件:行 | 覆盖 | 未覆盖 |
|---|---|---|---|
| `TestGenApp_TemplateFormats` | `app_test.go:31-59` | 渲染 + gofmt 成功；9 个原语字符串锚点存在；`store.UnregisterTenant("tenant_id")` 必须可执行 | 只测"字符串存在"；且**该锚点本身可疑**：模板里这条 Effect 的逆操作对应的正向注册（`store.RegisterTenant`）模板并未执行，注释说"配合业务 BeforeStart"（`app.go:70-76`、测试 `app_test.go:50-58`）——即生成物默认带一条"无正向操作的逆操作" |
| `TestGenApp_GeneratedCodeCompiles` | `app_test.go:63-88` | 在 `_example/bald`（消费者 module）内 `go build` 单文件通过 | 不运行；不检查输出目录/分层；不检查 `buildServers` 是否被调用（死代码通过编译） |
| `TestGenAppSpec_TemplateFormats` | `app_test.go:93-139` | 8 个锚点；装配顺序两条 P0（`New` 在所有 `append` 之后、无 `.MountComponent(`） | 不校验声明字段是否**全部**被消费（gateway 漏消费不会被发现）；不校验生成物与 bconf 键一致 |
| `TestGenAppSpec_GeneratedCompiles` | `app_test.go:143-171` | http+grpc / grpc-only 两分支编译 | 仍只编译；grpc-only 分支的 `Capability.Requires` 只断言编译 |
| `TestGenAppSpec_GeneratedRuns` | `app_test.go:177-227` | 无传输 server 时运行 3s 存活（装配顺序 P0 的运行时防线） | 不开端口；不覆盖 http/grpc 实际启动；不覆盖 store/stream 审计、非 heartbeat 组件、gateway |
| `TestGenAppSpec_CommandOmitsName` | `app_test.go:232-263` | spec 模式可省 `<name>` + 默认输出路径；模板模式缺 name 报错 | 不覆盖 AppSpec 语义错误（未知 kind、非法 backends） |
| `TestWriteFileGuarded_*` / `TestGenProto_CommandGuard` / `TestGenApp_CommandGuardForce` | `guard_test.go:20-147` | 防覆盖 + `--force` + 输出走 `Out` | **`gen store` 无任何命令级测试** |

测试运行安全性评估（未运行，仅静态判断）：`app_test.go` 全部写盘都落在 `t.TempDir()`；`go build` 输出也在临时目录。唯一的仓库内相对写风险来自 `cmd.Dir = _example/bald` 时 go 工具链是否改写该模块 `go.sum`（Go ≥1.16 默认 `-mod=readonly` 会报错而非写）。结论：可跑，但本次未跑。

---

## 5. 文档与实现不一致（逐条）

| # | 文档 | 文档说法 | 实现 | 证据 |
|---|---|---|---|---|
| D1 | `代码生成工具设计.md:3,30` | AppSpec proto 在 `api/proto/bald/appspec/v1` | 实际 `bconf/proto/bald/appspec/v1/appspec.proto` | `Test-Path bald\api\proto\bald\appspec\v1\appspec.proto` = **False**；proto 实际路径已 read |
| D2 | `代码生成工具设计.md:33` | AppSpec 生成代码在核心 `pkg/conf/gen/go/bald/appspec/v1` | 实际 `bconf/gen/go/bald/appspec/v1`（且是**独立 module** `bald/bconf`） | `app.go:12` import `github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1`；`bconf/go.mod` 存在 |
| D3 | `代码生成工具设计.md:41` | `gen proto` 产物「含 `PagingRequest`/`FilterExpr` 引用」 | 模板只有 `PagingRequest`/`PaginationResponseMeta`，无 `FilterExpr` | `gen.go:47,52`（grep FilterExpr 在 codegen = 0） |
| D4 | `代码生成工具设计.md:106`、`guide/用 bald CLI 起步新服务.md:105` | 反向生成对照样本 `_example/bald/_scratch/go-bald-admin.appspec.json` | 该目录下只有 `appspec.sample.json` | `_scratch` 目录列表只返回 `appspec.sample.json`；`Test-Path …go-bald-admin.appspec.json` = False |
| D5 | `guide…:128` | 覆盖地址用 `--http.addr=:18080` | 生成物绑定前缀是 `server.http` → `--server.http.addr`（模板自己的注释也这么写） | `appspec_gen.go:123-125`；`pkg/appkit/bootstrap.go:805`；`bald-admin…assembly.go:388` |
| D6 | `guide…:143` | 生成物为 `OnKeyChange("http.addr", ...)` | 实际 `appkit.OnKeyChange("server.http.addr", ...)` | `appspec_gen.go:130` |
| D7 | `guide…:40` | 命令输出 `=> generated app (spec-driven): cmd/my-service/main.go` | 实际只输出 `generated: <path>` | `iostreams.go:52` |
| D8 | `guide…:113-120`（配置示例） | 配置文件写顶层 `http.addr` / `grpc.addr` | 生成物读 bconf `server.http.addr` / `server.grpc.addr`；写错的键会被**静默忽略**（DiscardUnknown）→ 用户按指南写配置会静默无效，回退到 spec 默认值 | `appspec_gen.go:81,124-125`；`bconf/unmarshal.go:151-153`；`pkg/appkit/bootstrap.go:805,808` |
| D9 | `代码生成工具设计.md:91` | 「gateway … 模板 TODO **改为生成该接线**（`WithGatewayRegister` + `server.http.driver`）」 | 模板仍无任何 gateway 接线（grep `Gateway` = 0），AppSpec 的 `gateway` 字段无消费者；guide `:73,169` 也承认"当前为业务 TODO" | `appspec_gen.go` 全文；`appspec.proto:52`；`bootstrap.go:178` |
| D10 | `代码生成工具设计.md:1,37-44`「三个子命令」表述 vs 任务书「四个子命令」 | 文档是对的 | 实现也是 3 个 + `--spec` 模式 | `gen.go:82` |
| D11 | `代码生成工具设计.md:46-49` / `guide…:181` 「共用 `writeFileGuarded`…不静默覆盖」 | 一律不覆盖 | `renderAppSpec` 在 gofmt 失败时直接 `os.WriteFile`（无 `--force` 也覆盖），文档仅在括注里提"落盘前的 gofmt 诊断性写盘除外" | `appspec_gen.go:357-362` vs `代码生成工具设计.md:49` |
| D12 | `guide…:139-145`「生成物…`server.NewHTTPServer`/`server.NewGRPCServerWithRegister`」 | 与模板一致（仅 alias 名 `httpserver`/`grpcserver` 不同） | 模板 `appspec_gen.go:60,65,68,163,169` | 低危，仅命名 |
| D13 | `代码生成工具设计.md:12,118`「生成器只做装配…反向生成 go-bald-admin 验证了边界」 | 反推的"边界"是：go-bald-admin 的**分层与业务桥接**无法生成 | 与 §4 的 G3/G5 一致，但文档未把"分层骨架 / 端到端示例链路"列为缺口，而目标形态要求它们 | `代码生成工具设计.md:82-92`（骨架边界表只列业务填充点，未列目录结构与示例链路） |

---

## 6. 未决问题 / 待考

1. `appspec_gen.go` 渲染产物的**精确行数**未实测（未运行 gofmt/测试）；本文给的是模板源行数（311）与条件块清单。全开估算 ≈300 行。
2. 任务书提到的 "bconf 的 `UnmarshalMap`/`Store` 用法"：`UnmarshalMap` 已核实（`bconf/unmarshal.go:126`）；**bconf 无 `Store` 类型或函数**（bconf 导出面：`NewBootstrap`/`Validate`/`BindFlags`/`UnmarshalMap`/`FindUnknownKeys`）。若指 `audit` 的 `store` 后端或 `pkg/store`，请指明——本文按 `audit.store` 后端与 `bootstrappkg.DB` 处理。
3. `_example/bald/_scratch/appspec.sample.json` 对应的生成物未在仓库留存，无法比对"声明 → 生成物"的实际差异。
4. 任务书提及的 `internal/codegen/app.go` 184 行 / `appspec_gen.go` 281 行 / "四个子命令"，与工作树不符；若审计对象是某个历史 revision，本文结论需按其版本复核（本文全部基于当前工作树）。
5. `go test ./internal/codegen/...` 未运行（静态评估无仓库写盘，但为守"只读审计"未执行）；`TestGenAppSpec_GeneratedRuns` 的 3s 存活断言在 CI 的稳定性（慢机器上 Run 未就绪）待考。
