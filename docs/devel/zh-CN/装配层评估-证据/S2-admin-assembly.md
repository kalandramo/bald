# S2 · bald-admin 装配路径实测（只读审计）

审计对象：`D:\code\konglingfei\bald-admin\backend`（module `github.com/kalandramo/bald-admin`，
依赖已发布 `bald v0.24.0` + 40 余个 submodule tag）。
参照物：`D:\code\konglingfei\bald`（框架本体，只用于引用契约定义点）。
本报告未修改任何仓库文件；未执行 `go build` / `go test`。

---

## 0. 方法与口径（先读这段，否则数字对不上）

**行数口径**：本报告所有行数 = 文件真实行数（含空行、含注释）。
实测方法：node `fs.readFileSync().split('\n')` 去掉末尾空元素；与 .NET `File.ReadAllLines()`
交叉核对一致（`assembly.go` = 623 行，`bootstrap.go` = 916 行）。
**`read` 工具的行号与上述真实行号一致，本报告全部 `file:line` 以此为准。**

**与任务书基线数字的差异（待考）**：任务书给出 `bootstrap.go 639 / assembly.go 399 /
options.go 275 / registries.go 159 / audit.go 158`。这 5 个数字与「非空行数」也不相等
（实测非空行为 583 / … ）。我在本机 PowerShell 上复现出**同样的一组数**：
`Get-Content <file> | Measure-Object -Line` 对该文件返回 435（assembly.go）、681（bootstrap.go），
再取非空行为 399 / 639——即基线来自一个**会少算总行数**的计数通道。
其机制未查明（`\r` 计数为 0，非 CRLF 问题），**标为待考**；不影响文件数、占比与结论方向。
`bald-admin/backend` 不含生成物的 Go 文件数实测 **116 个**，与基线一致（文件集合口径一致）。

**三分法口径（本报告自定，判定依据见 §2）**：

| 类别 | 定义 | 文件数 | 真实行数 |
|---|---|---|---|
| ASSEMBLY | `cmd/**`（非测试）+ `internal/bootstrap/**`（非测试）+ `internal/apiserver/{server.go,bizset.go}` | 19 | 3,787 |
| BUSINESS | `internal/apiserver/{biz,model,handler}/**`（非测试） | 81 | 12,760 |
| INFRA（业务侧基础设施） | `internal/security/**`、`internal/cachekit/**`、`internal/apiserver/middleware/**`（非测试） | 16 | 1,744 |
| TEST | 全部 `_test.go`（含 `internal/apiserver/e2e/**` 45 个） | 70 | 11,881 |
| GENERATED | `api/gen/go/**` | 39 | 22,185 |

判定理由：ASSEMBLY = 「不写它业务逻辑照样存在、但删了进程起不来」的文件，且**新增业务域会去改它**。
`internal/bootstrap/*` 是 DI/桥接根（bootstrap.go:261 `InitBridges`），
`internal/apiserver/server.go` 是路由挂载点（server.go:25 `RegisterRoutes`），
`internal/apiserver/bizset.go` 是业务对象聚合容器——三者都随业务域增长而修改（§4 实证）。

---

## 1. 启动链路逐跳还原

`main.go:27 → app.Execute() → NewCommand() → cobra RunE=serveRunE → buildApp → newApp → appkit.FromBootstrap → app.Run`
`Run` 期再由框架按「注册序=执行序」触发 `WithBeforeStart`（业务装配）与各 server provider 回调。

| # | 跳 | 文件:行 | 职责 | 该文件真实行数 |
|---|---|---|---|---|
| 1 | `main` | `cmd/bald-admin/main.go:26-29` | 只调 `app.Execute()`，非零退出码 | 30 |
| 2 | `Execute` | `cmd/bald-admin/app/assembly.go:201-218` | kubectl 风格插件发现（`bald-admin-<name>` 转发到 PATH），再 `root.Execute()` | 623 |
| 3 | `NewCommand` | `assembly.go:187-195` | 只建 cobra root，**不注册任何 flag**（`FParseErrWhitelist.UnknownFlags=true`），RunE=`serveRunE` | 623 |
| 4 | `serveRunE` | `assembly.go:63-97` | 阶段 0 契约对象 `bconf.NewBootstrap()`(:65)、业务配置 `options.NewServerOptions()`(:69)、阶段 1 预装载(:76)、阶段 2 `buildApp`(:86)、阶段 3 `app.Run(ctx)`(:92) | — |
| 5 | `preloadBootstrap` | `assembly.go:76` → `registries.go:99-119` | 两阶段装载第一阶段：自建 pflag 扫 `os.Args` 取 `--config`(:103-106)，`baldconfig.Load`(:110) 只填契约 `config` 段引导信息；返回 `configRegistry()` | registries.go 228 |
| 6 | `bundle.FromMiddleware` | `assembly.go:150-155` | 契约 `server.http.middleware.*` 段 → bundle Option；非法即构造期 fail-fast | — |
| 7 | gin 全局链 | `assembly.go:157-163` | `bundle.New(Audit→Metrics→Normalized + mwOpts...)` → `gin.New()` → `router.Use(ginBundle.Gin()...)`；链序由 bundle 固化 | — |
| 8 | `buildApp` | `assembly.go:105-171` | 构造期只建伺服面骨架 + `health.New()`(:117) + 组件工厂目录(:123-125) + `appRefT`(:122)，调 `newApp`(:165)，回填 `appRef.set(app)`(:169) | — |
| 9 | `newApp` | `assembly.go:250-623` | 组装 `~25 个 appkit BootstrapOption`：能力声明 `WithHTTP/WithGRPC/WithGRPCOptions/WithHealth`(:311-319)、日志装饰(:330)、配置 `WithConfigFile/WithWatchConfig/WithStrictContract/WithBind/WithConfigRegistry`(:337-348)、六域注册表 `WithRegistrarRegistry/WithDatabaseRegistry/WithCacheRegistry/WithStorageRegistry/WithTracerRegistry/WithMetricsRegistry`(:352-364)、`WithServerRegistry(serverRegistry(...))`(:373)、能力声明 `WithProvides("db")/WithRequires("audit.store","db")`(:382-383)、`WithOnKeyChange`(:388)、`WithEffect`(:397)、`WithBeforeStart`(业务装配体 :403-562)、`WithReconcile("audit.backends",…)`(:568)、`WithAfterStart/WithBeforeStop`(:570/:579)、`WithAuditRegistry`(:608)、`WithConfigNamespace`(:614) | — |
| 10 | `appkit.FromBootstrap` | `assembly.go:616` | 框架内化 Bind×3 / BeforeStart(配置装载+校验+两阶段日志) / OnConfigChange / registrar / 可观测性 / 停机 Effect | — |
| 11 | `app.Run` | `assembly.go:92`（定义 `bald/pkg/appkit/appkit.go:477`） | 阻塞至信号；期间按 Run 期顺序回调 | — |
| 12 | `WithBeforeStart` 钩子体 | `assembly.go:403-562` | 见下面 12a–12k | — |
| 12a | 业务配置解码+校验 | `assembly.go:407-412` → `options.go:195 / :353` | `svrOpts.DecodeInto(app.Settings())` 只解 `biz` 子树、`ErrorUnused=true`；`Validate()` 聚合报错 | options.go 399 |
| 12b | 契约实例注入桥接 | `assembly.go:415-427` | `app.Database("sql")/Cache("redis")/Storage("minio"/s3)` → `bootstrap.WireDatabase/WireCache/WireStorage` | — |
| 12c | 注入契约配置+审计兜底租户 | `assembly.go:431/436` | `bootstrappkg.Configure(bootstrap, svrOpts.File.Bucket)`；`securityaudit.SetFallbackTenant`（注释明示**必须在 audit store 构造前**） | — |
| 13 | `InitBridges` | `assembly.go:440` → `bootstrap.go:261-454` | 桥接根：RSA 密钥对(:271-284) → `openDB`(:294→:849) → `AutoMigrate` 26 张表(:298-309) → `sqlaudit.Register(db)`(:317) → Redis(:324-332) → MinIO(:337-353) → **`Repositories` 33 个 `store.NewStore[T]`**(:355-412) → `seed`(:414→:457) → `store.RegisterTenant("tenant_id",…)`(:421) → `loadPolicyCSV`(:427→:894) → `casbinauthz.New`(:431) → `NewReloadable`(:439) → `NewObjectAlias(…, grpcObjectAliases)`(:447) | bootstrap.go 916 |
| 14 | `buildTokenStore` | `assembly.go:452` → `builders.go:123-128` | 有 Redis 才建；注释明示**必须在路由注册前**否则吊销静默失效 | builders.go 128 |
| 15 | `InitializeBiz` | `assembly.go:468` → `assembly_biz.go:56-125` | 手写 DI：逐域 `xxx.New(repos.…)` 20 个 biz(:57-99) + `BizSet` 字面量 21 个字段(:102-123) | assembly_biz.go 145 |
| 16 | `RegisterRoutes` | `assembly.go:474` → `server.go:25-82` | 挂 23 个域的路由：`hgin.RegisterDict`(:46) 等 21 行 + api 审计中间件(:37) | server.go 92 |
| 17 | `registerAdminRoutes` | `assembly.go:476` → `components.go:85-89` | 管理面组件挂载/卸载路由 | components.go 89 |
| 18 | 运行期补注 | `assembly.go:488-560` | `SetObjectStorage/SetStorage`(:489/:491)、`SetCache`(:501-502)、`buildLoginLimiter/Breaker/Retrier`(:508/:515/:520→`builders.go:20/:50/:84`)、`SetPlatformResolver`(:531)、`SetTokenStore/SetAuthenticator/SetCaptchaStore/SetMFA`(:539-547)、`SetTokenTTL`(:559) | — |
| 19 | gRPC service 注册 | `assembly.go:274-288`（由框架在 Run 期 server 构造点回调） | 11 个 `RegisterXServiceServer`（按服务，非按域：Dict 域 2 个） | — |
| 20 | gRPC 拦截器链 | `assembly.go:315` → `servers.go:29-59` | `bundle.New(Authn→Authz→Audit→Metrics→Normalized)`(:37-47)，尾部**自行 append** `grpcmw.ValidatorInterceptor`（注释：bundle 无校验层位置）(:57-58) | servers.go 103 |
| 21 | server provider 回调 | `assembly.go:373` → `registries.go:169-206` | `ServerRegistry` 注册 4 个协议域 provider：asynq(:176-190)、cron(:192-196)、gateway(:198)、sse(:203)；各自的业务接线都在回调内（asynq→`task.SetScheduler`、cron→`registerCronJobs`、sse→`wireMessageSSE`） | registries.go 228 |
| 22 | 审计 provider | `assembly.go:608` → `audit_wiring.go:54-115` | `store`(:74-89)/`stream`(:97-114) 两个后端 provider；`resolveAuditRedis` 注释说明必须**消费时点**求值否则固化 nil(:91-130) | audit_wiring.go 146 |
| 23 | 审计协调 | `assembly.go:568` → `audit.go:133-174` | `ReconcileCtx` 期望态 diff：`rctx.String("audit.backends")` vs `rctx.Mounted()`，逐后端 `Mount/Unmount`；组件实现见 `audit.go:55-93` | audit.go 204 |
| 24 | 启动完成钩子 | `assembly.go:570-578` | `WithAfterStart` 打日志 | — |

**链路结论**：从 `main.go` 到「服务器起来」共 **24 跳、跨 19 个装配文件、3,787 行装配代码**，
其中 `newApp` 一个函数 374 行（`assembly.go:250-623`），承载 ~25 个框架 Option。

---

## 2. 装配 vs 业务量化

**划分口径**（同 §0）。装配 = `cmd/**` + `internal/bootstrap/**` + `apiserver/{server.go,bizset.go}`。

| 类别 | 文件数 | 真实行数 | 占非生成非测试手写代码 |
|---|---|---|---|
| ASSEMBLY | 19 | 3,787 | **20.7%** |
| BUSINESS（biz+model+handler） | 81 | 12,760 | 69.6%（其中 `biz/v1` 7,077 行 = 38.6%） |
| INFRA（security/cachekit/middleware） | 16 | 1,744 | 9.5% |
| 小计（非生成非测试） | 116 | 18,291 | 100% |

配套口径（同一批装配文件，去空行后）：总行 3,787、空行 277、**注释 1,392**、有效代码 2,118；
**注释占非空行 40%**——即「3,787 行装配」里约有 1,392 行是脚手架自述文档，
不是可执行装配逻辑。但 **21 个业务域**对应的可执行装配逻辑仍然随域线性增长（§4 量化）。

### 2.1 `cmd/bald-admin/**` 20 个文件逐个定性

（`cmd/bald-admin/app/` 目录本身 18 个文件；含 `main.go` 与 `app/options/` 共 20 个。
非测试 14 个 / 2,519 行，测试 6 个 / 827 行。）

| 文件 | 行数 | 定性 | 业务决策证据（file:line） |
|---|---|---|---|
| `app/assembly.go` | 623 | **纯装配（编排）** | 唯一「疑似业务」的是链序与能力声明：全局链(:157-163)、`WithProvides("db")/WithRequires("audit.store","db")`(:382-383)、gateway 独立端口注释(:585-587) |
| `app/options/options.go` | 399 | **挟带业务决策（配置 schema 全文）** | 限流 rate/burst(:93-96)、熔断 error_threshold/request_volume/sleep_window(:99-103)、重试 max_attempts/backoff(:106-110)、令牌 TTL(:123-126)、`file.bucket`(:113-115)、`audit.fallback_tenant="t-default"`(:240)、asynq codec(:129-131)、SSE addr/path(:140-143)、`BusinessSection="biz"`(:180)、flag 绑定 11 项(:304-347)、校验规则(:353-397) |
| `app/audit.go` | 204 | **挟带业务决策** | 审计后端期望态取值域 `log/store/stream` 硬编码(:183)、MultiAuditor 排序语义(:32-49)、stream 名/buffer 缺省(:99-100) |
| `app/audit_wiring.go` | 146 | 装配 + 决策 | 注册 `store`/`stream` 两个后端(:74/:97)、`stream` 无 Redis 时 **fail-fast 而非降级**(:101-103)、`resolveAuditDB` 回退包级 DB(:137-145) |
| `app/sse.go` | 210 | **挟带业务决策（安全语义）** | SSE 授权语义「stream 必须等于 token subject」(:118-142)、`WithAutoStream(true)`/`WithAutoReplay(true)` 开关(:95/:97)、message 域发布适配(:157-209) |
| `app/registries.go` | 228 | **纯装配（provider 表）** | 8 个注册表函数(:29 tracer/:43 metrics/:56 config/:127 registrar/:139 database/:145 cache/:169 server/:221 storage，第 9 张 AuditRegistry 在 `audit_wiring.go:54`)、`configNamespace="bald-admin"`(:75)、`configFileDefault`(:86)、两阶段预装载(:99-119) |
| `app/assembly_biz.go` | 145 | **纯装配**（但逐域线性增长） | 20 个 `xxx.New(...)`(:57-99) + `BizSet` 字面量(:102-123) |
| `app/builders.go` | 128 | **挟带业务决策** | 限流器构造与禁用判据(:20-34)、熔断阈值语义（并记录为何弃 sres 用 hystrix :36-48）、重试退避与「只重试瞬时故障」分类器 `authbiz.RetryOnTransientDBError`(:84-111)、TokenStore 降级条款(:113-127) |
| `app/servers.go` | 103 | 装配 + 决策 | gRPC 链尾追加校验层且理由成文(:48-58)、gateway 逐服务登记 11 行(:66-102) |
| `app/asynq.go` | 89 | 装配 + 业务探针 | codec 幂等注册(:58-65)、探针任务 `bald:probe` handler(:71-88) |
| `app/components.go` | 89 | 装配 + 演示组件 | `demo.heartbeat` 组件（Start 起 5s 心跳 goroutine / Dispose 等退出）(:48-81) |
| `app/scheduler.go` | 73 | 装配（接口适配器） | `asynq.Server` → `taskbiz.Scheduler` 四方法适配(:39-58)，含同名 Option 陷阱说明(:15-31) |
| `app/cron.go` | 52 | **挟带业务决策** | 注册周期任务 `*/30 * * * * *` 探针(:44-46)；6 字段 vs asynq 5 字段的差异(:18-26) |
| `main.go` | 30 | **纯装配** | 仅入口(:26-29) |
| `app/assembly_e2e_test.go` | 141 | 测试（装配回归） | `TestAssemblyPath_ReadyAfterStart` |
| `app/assembly_middleware_test.go` | 237 | 测试（装配回归） | MiddlewareFromConfig / OnBusinessRoute / InvalidMiddlewareFailsFast |
| `app/reconcile_audit_test.go` | 119 | 测试 | ReconcileAudit 4 例 |
| `app/audit_stream_test.go` | 104 | 测试 | StreamProviderRegistered |
| `app/testaddr_test.go` | 65 | 测试 helper | 动态空闲端口 |
| `app/options/options_test.go` | 161 | 测试 | 5 例（含「AddFlags 不得把缺省段变启用」） |

**结论**：`cmd/bald-admin` 的 20 个文件里，**纯装配 4 个**（assembly.go、registries.go、
assembly_biz.go、main.go）、**挟带业务决策 8 个**（options.go、audit.go、audit_wiring.go、
sse.go、builders.go、cron.go、servers.go、asynq.go 部分）、**演示/适配 2 个**
（components.go、scheduler.go）、**测试 6 个**。
即：业务参数（限流/熔断/重试阈值、审计后端集合、cron 表达式、SSE 安全语义、令牌 TTL、
对象存储桶名、codec）实际住在装配目录里，而不是 `internal/apiserver/biz/**`。

---

## 3. 判据 3 实测：业务层是否 import 框架内部包

扫描面：`internal/apiserver/biz/**` 全部 `.go`（31 个，非测试 25 个）。

**先给事实，再给两种读法。**

事实 A：业务层 import 的 `github.com/kalandramo/bald/**` 包（非测试文件）共 **15 个不同包、62 条 import**：

| 次数 | 包 | 代表文件 |
|---|---|---|
| 19 | `bald/pkg/store` | `biz/v1/auditlog/auditlog.go`、`biz/v1/user/user.go` |
| 11 | `bald/berrors` | `biz/v1/dict/dict.go`、`biz/v1/menu/menu.go` |
| 10 | `bald/bconf/gen/go/bald/store/v1` | `biz/v1/auth/auth.go`、`biz/v1/task/task.go` |
| 4 | `bald/pkg/contextx` | `biz/v1/dict/dict.go`、`biz/v1/secret/secret.go` |
| 2 | `bald/log` | `biz/v1/auth/auth.go`、`biz/v1/cachemonitor/cachemonitor.go` |
| 2 | `bald/pkg/audit` | `biz/v1/auth/auth.go`、`biz/v1/permission/permission.go` |
| 2 | `bald/cache` / 2 | `bald/cache/loadable`（`biz/v1/dict/dict.go`、`biz/v1/secret/secret.go`） |
| 2 | `bald/oss/minio`（`biz/v1/file/file.go`、`file/filestore/minio.go`）、`bald/oss/s3`（`file/filestore/s3.go`） | |
| 各 1 | `bald/circuitbreaker`、`bald/contrib/authn-jwt`、`bald/pkg/authn`、`bald/ratelimit`、`bald/retry`（集中在 `biz/v1/auth/auth.go:19-31`） | |

25 个非测试 biz 文件中 **22 个**至少 import 一个 bald 包。

事实 B：业务层 import 的 `internal/...` 包**全部是同 module 兄弟包**，无一条是框架内部包：
`bald-admin/internal/apiserver/model`、`internal/cachekit`、`internal/security/{captcha,loginpolicy,mfa,token}`、
`internal/apiserver/biz/v1/file/filestore`。零条 `bald/internal/...`（跨 module 引用 `internal` 在 Go 中本就不可能编译）。
非测试 biz 文件中**零条** import `internal/bootstrap`（装配根）。
反证案例（测试文件不在约束内）：`biz/v1/permgroup/permgroup_id_test.go`、
`biz/v1/secret/secret_test.go`、`biz/v1/auth/asymmetric_e2e_test.go` 三个测试文件 import 了
`internal/bootstrap`。

事实 C：仓库自带的机械化守卫只锁一条边界：
`internal/apiserver/layering_invariant_test.go:34-69` 断言「biz 非测试文件不得 import
`bald-admin/internal/bootstrap`」，且**显式跳过 `_test.go`**（:49-51）。
文件头注释自述历史上清掉了「20 个 biz 包对 bootstrap 的 135 处依赖」（:8-11）。

**判定**：
- 按任务书口径的前半句（`bald/**` 下**非契约包**）——需要先定义「契约包」。仓库自身用
  `xxx/contract` 子包表达契约（`transport/{asynq,cron,gateway,sse}/contract`、
  `contrib/{audit-stream,observability-otlp}/contract`、`registry/nacos/contract`、`oss/s3/contract`）。
  按这个定义，`bald/pkg/store`、`pkg/contextx`、`berrors`、`cache/loadable`、`bconf/gen/**`
  **都不是** contract 子包，却出现在 22/25 个业务文件里 → **判据 3 不成立**。
  但这些是框架刻意暴露的公共库（`pkg/**` 非 `internal/**`），把它们算「内部包」会失之过严。
- 按任务书口径的后半句（任何 `internal/...`）——业务层只 import 本 module 兄弟包，
  跨 module 的 `internal` 不可能；**判据 3 实质成立**，唯一真实耦合是
  「业务层直接依赖 `bald/pkg/store` 泛型仓储 + `bconf/gen` 契约类型」，属框架公开 API。

**给框架作者的可操作结论**：判据 3 的措辞需要先钉死「契约包」的定义。
若采用「`pkg/**` 与 `bconf/gen/**` 算契约」，则判据 3 成立；若采用「只有 `*/contract` 算契约」，
则有 15 个包、62 条 import、22/25 个业务文件不达标。

---

## 4. 判据 1 实测：新增一个业务域到底动几个文件

### 4.1 实证：git 里真实存在一次「加域」提交

```
89f14e5  feat(permgroup): Wave 4.1 权限组 + 策略评估日志（源 7 rpc）   Sun Sep 20 2026
backend/cmd/go-bald-admin/wire_gen.go                      ← 装配（今日对应 cmd/bald-admin/app/assembly_biz.go）
backend/internal/apiserver/bizset.go                       ← 装配（BizSet 加字段）
backend/internal/apiserver/server.go                       ← 装配（RegisterRoutes 加一行）
backend/internal/bootstrap/bootstrap.go                    ← 装配（AutoMigrate + Repositories + NewStore + 授权策略行）
backend/internal/apiserver/biz/v1/permgroup/permgroup.go   ← 新域：业务
backend/internal/apiserver/handler/gin/permgroup.go        ← 新域：REST
backend/internal/apiserver/model/permission_group.go       ← 新域：模型
backend/internal/apiserver/e2e/t_w4_1_permgroup_e2e_test.go← 新域：测试
```

**8 个文件（含 1 个测试），其中 4 个是装配文件**（`wire_gen.go`→今 `assembly_biz.go`、
`bizset.go`、`server.go`、`bootstrap.go`）= 50% 是装配。
注：permgroup 无 proto、无 gRPC handler（REST-only 域），故这是**最省的下界**。

### 4.2 静态实测：一个「全栈域」（dict，proto+gen+REST+gRPC+gateway）的完整触碰集

`api/gen` 是 **git 跟踪**的（`git ls-files backend/api/gen` = 39 个文件），
生成物也在触碰集里。全仓 `dict`（剔除 `PeriodicTask` 之类假阳性）命中的文件：

| # | 文件 | 类别 | 字典取值证据 |
|---|---|---|---|
| 1 | `api/protos/dict/v1/dict.proto` | proto（新） | buf 源；`api/buf.yaml`、`api/buf.gen.yaml` 为生成配置 |
| 2-4 | `api/gen/go/dict/v1/{dict.pb.go,dict.pb.gw.go,dict_grpc.pb.go}` | 生成（新，3 个） | 52KB/46KB/27KB |
| 5 | `internal/apiserver/model/dict.go` | 业务（新） | 模型 |
| 6 | `internal/apiserver/biz/v1/dict/dict.go` | 业务（新） | 49 处 dict 引用 |
| 7 | `internal/apiserver/handler/gin/dict.go` | 业务（新） | `RegisterDict` @:38，10 条 REST 路由 @:51-` |
| 8 | `internal/apiserver/handler/grpc/dict.go` | 业务（新） | 146 处引用 |
| 9 | `internal/apiserver/handler/convert/model.go` | 共享转换层（改） | `DictTypeToPB` @:83，import `dictv1` @:15 |
| 10 | `internal/apiserver/bizset.go:39` | **装配** | `Dict *dictbiz.Biz` |
| 11 | `internal/apiserver/server.go:46` | **装配** | `hgin.RegisterDict(e, authenticator, authorizer, biz.Dict)` |
| 12 | `cmd/bald-admin/app/assembly_biz.go:69,109` | **装配** | `dict.New(...)` + `BizSet.Dict:` |
| 13 | `cmd/bald-admin/app/assembly.go:280-281` | **装配** | 两个 `RegisterDictType/EntryServiceServer` |
| 14 | `cmd/bald-admin/app/servers.go:83-88` | **装配** | 两个 `RegisterDictType/EntryServiceHandler`（gateway 转码） |
| 15 | `internal/bootstrap/bootstrap.go` | **装配** | `Repositories.DictType/DictEntry`(:196-197)、`AutoMigrate`(:300)、`NewStore`(:364-365)、`seed` 字典种子(:563-588)、`Permission` 种子(:549-550)、`seedPolicies` 12 行(:760-776) |
| 16 | `internal/security/authz/alias.go` | **装配**（授权别名） | gRPC 归一化名 `dicttype`/`dictentry` → 策略名 `dict_type`/`dict_entry`（对应 `bootstrap.go:70-77` 的 `grpcObjectAliases`） |
| 17 | `internal/apiserver/assets/openapi.yaml` | 生成（改） | 118 处 dict（由 `api/buf.openapi.gen.yaml` 生成） |

**计数结论**：
- 新增（新文件）：**8 个**——1 proto + 3 gen + model + biz + gin + grpc（convert 属共享层修改）。
- 必须修改的既有文件：**9 个**——`bizset.go`、`server.go`、`assembly_biz.go`、`assembly.go`、
  `servers.go`、`bootstrap.go`、`security/authz/alias.go`、`convert/model.go`、`openapi.yaml`(生成)。
- 其中**装配文件 6 个**（`bizset.go`、`server.go`、`assembly_biz.go`、`assembly.go`、`servers.go`、
  `bootstrap.go`）+ 授权装配 1 个（`alias.go`）。
- 总计触碰 **≈15–17 个文件，装配占 6–7 个（约 40–45%）**。
- 最省的 REST-only 域（无 proto、无 gRPC，即 permgroup 反例）仍需 **7 个非测试文件 / 4 个装配文件**。

**判据 1 判定：不成立。** 任务书判据是「3 个文件、零装配修改」；实测下界是
「7 个文件 / 4 个装配文件必须改」，全栈域是「15+ 个文件 / 6–7 个装配文件必须改」。
**零装配修改在现有结构下不可能**：不注册 `bizset.go` 的字段编译不过，不挂 `server.go` 的路由请求进不来，
不建 `bootstrap.go` 的 store 与 `AutoMigrate` 表跑不起来，不写 `seedPolicies` 策略行新域恒 403。

### 4.3 判据 2 实测：装配行数是否随业务域数增长

对 20 个域关键词在 6 个装配文件里做逐域引用计数（非测试，含注释）：

| 装配文件 | 全仓域相关引用数 | 每域平均 |
|---|---|---|
| `internal/bootstrap/bootstrap.go` | 129 | ~6.5 |
| `internal/apiserver/server.go` | 47 | ~2.4 |
| `cmd/bald-admin/app/assembly_biz.go` | 40 | ~2.0 |
| `cmd/bald-admin/app/assembly.go` | 34 | ~1.7 |
| `internal/apiserver/bizset.go` | 32 | ~1.6 |
| `cmd/bald-admin/app/servers.go` | 11 个 `Register*ServiceHandler`（每服务 1 个 if-err 块 ≈3 行） | ~3 |
| **合计（不含 servers.go）** | **282 处域相关装配引用** | **≈14 处/域** |

（`servers.go` 的 11 个 gateway 登记块按 service 计、不按域名命中，故未计入 282；
若计入，每域平均装配改动 ≈ 17 处。）

另外 `assembly.go:274-288` 有 11 个 gRPC service 注册、`servers.go:66-102` 有 11 个 gateway 注册
（按 **service** 而非按域，Dict/Org 各占 2 个）。

**判据 2 判定：不成立。** 增加 1 个域约新增 **14 行装配**（6~7 个文件各改一处）；
21 个域的现状是 3,787 行装配。若目标是「装配行数不随域数增长」，需要的是
注册表/声明式装配（如 `init()` 注册制或代码生成），当前是逐域手工登记。

---

## 5. 开发者必须理解的框架概念清单

「是否判据 1 必需」= 不加理解就无法完成「新增一个业务域到路由可用」。

| # | 概念 | 现场证据（file:line） | 判据 1 必需？ |
|---|---|---|---|
| 1 | `store.Store[T]` 泛型仓储 + 主键函数 + `WithPlatformLevel` + `WithPageSize` | `bootstrap.go:355-412`（33 个 `NewStore`）、`:196-228`（`Repositories` 字段）、`:421`（`store.RegisterTenant`）；业务侧 `biz/v1/dict/dict.go` import `bald/pkg/store` | **必需** |
| 2 | 配置命名空间 + `biz` 段 + 两阶段装载（引导段本地可得） | `options.go:180`（`BusinessSection="biz"`）、`options.go:195-222`（`DecodeInto` 只解 biz + `ErrorUnused`）、`assembly.go:342`（`WithStrictContract(options.BusinessSection)`）、`assembly.go:346`（`WithBind`）、`registries.go:75/:86`（namespace 与文件路径同源）、`registries.go:99-119`（预装载） | 仅当新域带配置项 |
| 3 | `StrictContract`（未知键 fail-fast，白名单由应用给） | `assembly.go:339-342`；定义 `bald/pkg/appkit/bootstrap.go:298` | 仅当新域带配置项 |
| 4 | casbin 授权策略归一化：REST→HTTP 动词小写、gRPC→get/list/write；object 名与 Service 名不一致 | `bootstrap.go:614-826`（策略行，注释 `:648-654` 记录「此前只有四条 → List/Create 403」）、`bootstrap.go:70-77`（`grpcObjectAliases`）、`internal/security/authz/alias.go`；双协议六元组要求见 `:732-740` | **必需**（否则新域恒 403） |
| 5 | `bundle` 链序（gin 全局链 / gRPC 链）与「bundle 无校验层位置」 | `assembly.go:157-163`、`servers.go:37-59`（尾部自行 append validator）；定义 `bald/pkg/middleware/bundle/bundle.go:158/:171/:224` | 部分必需（要挂鉴权/审计的域） |
| 6 | `ReconcileCtx` 期望态/实际态 diff + `Mount/Unmount/Mounted` + `appkit.DiffStrings` | `audit.go:133-174`；定义 `bald/pkg/appkit/reconcile.go:63` | 否（仅审计热切换） |
| 7 | `appkit.Component` 生命周期（`Name/Start/Dispose`） | `components.go:48-81`（`demo.heartbeat`）、`audit.go:55-93`（审计后端组件）；定义 `bald/pkg/appkit/component.go:31` | 否 |
| 8 | `transport.Server` 契约 + provider 签名 `(ctx, *bootstrapv1.Server) (transport.Server, func(), error)` | `sse.go:71`、`asynq.go:71-75`（类型断言）；定义 `bald/transport/server.go:16` | 否（仅新协议轴） |
| 9 | 注册表制 provider（Tracer/Metrics/Database/Cache/Storage/Server/Audit/Registrar/Config）：未注册后端即 fail-fast | `registries.go:29-227`（9 个 `*Registry` 函数）、`audit_wiring.go:73-115`、`assembly.go:352-373/:608`；`bald/bootstrap/registrar.go:61` | 否（仅新后端） |
| 10 | `Effect`（停机逆序回放） | `assembly.go:397-399`（`WithEffect("http-middleware", mwClose)`）；定义 `bald/pkg/appkit/bootstrap.go:410` | 否 |
| 11 | 能力声明 `WithProvides/WithRequires`（启动期校验依赖） | `assembly.go:382-383`（`"db"` / `"audit.store"`）；定义 `bald/pkg/appkit/bootstrap.go:428/:433` | 否 |
| 12 | `WithExtraServerFunc` 逃生舱（Run 期构造） | `bald/pkg/appkit/bootstrap.go:239`；`sse.go:17-20` 记录「仍是合法扩展点」 | 否 |
| 13 | Run 期时序契约：业务 beforeStart **先于** server provider 回调 | `assembly.go:256-263`（「注册序=执行序」）、`:465-467`；`registries.go:164-166`（用 getter 而非传值） | 部分必需（要接可选能力时） |
| 14 | 全局 codec 注册表 + `MustRegister` 不幂等 | `asynq.go:46-65`、`sse.go:50-65`（两处各自做幂等包装） | 否 |
| 15 | `appkit.AppKit` 资源取用（`Database/Cache/Storage/Settings/ListComponents`） | `assembly.go:407/:415-427`、`components.go:70` | 部分必需 |

**合计：新增一个业务域必须理解 4 条硬概念**（#1 仓储与租户隔离、#2/#3 业务配置段与严格校验、
#4 授权归一化与策略行、#5 链序/路由分组鉴权），其余 11 条只在「接新后端/新协议轴」时才有需要。
其中 #4 是纯框架契约衍生的负担——**没有它，代码编译通过、路由挂上、请求 403**。

---

## 6. 归因分层

### (i) 由 bald 框架模块导致的

| 证据 | 说明 |
|---|---|
| 两阶段配置装载样板 25 行 | `registries.go:99-119` 必须自建 pflag 扫 `os.Args`、自己 `baldconfig.Load`；`registries.go:69-74` 注释解释为何不能取契约 `app.name`（自指会让 env 前缀静默失效） |
| provider 注册制 ×9 张注册表 | `registries.go:29-227`；每个可选后端都要 `MustRegister`，未注册即 fail-fast（`assembly.go:154`、`sse.go:5-9`） |
| 审计双轨（契约轨 + 协调轨） | `audit_wiring.go:1-33` 头注释自述历史 bug（provider 未注册 + 时序倒置导致启动失败），`audit_wiring.go:91-96` 记录「必须闭包内取，否则固化 nil」 |
| 全局 codec 注册表语义 | `sse.go:50-65` 记录 `MustRegister` 对已注册名 **panic**、未注册时 `GetCodec` 静默 nil |
| asynq 同名类型陷阱 | `scheduler.go:15-31`（`bald/transport/asynq.Option` vs `hibiken/asynq.Option`）、`:49-52`（`RemovePeriodicTask` 要 taskId 不是 entryID） |
| 授权归一化缺口 → 应用补丁 | `bootstrap.go:55-77`（别名表 6 条）、`bootstrap.go:648-654`（补六元组的实测理由）、`internal/security/authz/alias.go` |
| 生命周期时序约束（必须理解隐式顺序） | `bootstrap.go:79-82`、`:126-131`（lazy 适配器族被删的历史）、`assembly.go:444-455`（TokenStore 必须在路由注册前，否则吊销静默失效）、`:479-492`（file 存储补注） |
| `appkit` Option 面巨大 → 装配函数 374 行 | `assembly.go:309-583` 单函数 25 个 Option；定义散见 `bald/pkg/appkit/bootstrap.go:239/298/355/383/410/428/433` |

### (ii) 由 bald-admin 示例自身选择导致的

| 证据 | 说明 |
|---|---|
| 为演示「全接一遍」：21 个域、9 个可选后端 | 19 个装配文件 3,787 行，含 nacos/otlp(2)/minio/s3/asynq/cron/gateway/sse/三审计后端 |
| 演示物冒充业务 | `components.go:48-81`（`demo.heartbeat` 5s 心跳）、`cron.go:44-46`（`*/30` 探针）、`asynq.go:71-88`（`bald:probe` handler）——共 230 行与业务无关 |
| 审计三后端 + 热切换双轨 | `audit.go` 204 行 + `audit_wiring.go` 146 行 = 350 行，只为演示 `audit.backends` 协调 |
| SSE 传输轴（示例为演示 SSE 而写） | `sse.go` 210 行，其中授权钩子/发布适配 120 行 |
| 业务种子数据写在装配根 | `bootstrap.go:457-609`（`seed`）+ `:614-826`（`seedPolicies`）= **369 行 = bootstrap.go 的 40%**，内容是租户/角色/菜单/权限点/字典/语言种子 + 130 行 casbin 策略 |
| 注释占比 40% | 19 个装配文件 3,787 行中注释 1,392 行（含大量「历史决策已被取代」段落，如 `asynq.go:3-31`、`cron.go:3-26`）——这是脚手架文档，不是装配负担 |

### (iii) 无法归因 / 待考

1. 任务书基线行数（639/399/275/159/158）与任何我认为合理的口径都对不上，
   我在本机复现出同源偏差但不清楚机制 → **待考**（不影响结论方向，见 §0）。
2. `permgroup` 提交是历史快照（当时文件名为 `cmd/go-bald-admin/wire_gen.go`）；
   「今日等价触碰集」是我做的映射（`wire_gen.go` → `app/assembly_biz.go` + `app/assembly.go`
   的 gRPC 段），未逐条 `git blame` 核对每个域的完整集合 → **待考**。
3. 判据 4「新服务 30 分钟内跑起来」需要真实动手计时，本次只读审计无法产出 → **未测**。
   代理指标：7 个文件下界 / 15+ 个文件全栈域 / 4 条硬概念 / 6 处隐式时序约束。
4. `openapi.yaml`（118 处 dict 引用）我判定为生成物（存在 `api/buf.openapi.gen.yaml`），
   未逐行验证其生成链 → **待考**。

---

## 附：本次审计的只读性声明

未修改 `D:\code\konglingfei` 下任何文件；未执行 `go build`/`go test`；未做任何 git 写操作
（仅 `git log` / `git show` / `git ls-files` 读操作）。读取的 `bald` 侧文件仅用于概念定义点引用。
