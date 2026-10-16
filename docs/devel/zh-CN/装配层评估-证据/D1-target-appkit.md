# D1 · appkit 目标设计（装配面收敛 + 目标 main.go）

范围：`D:\code\konglingfei\bald`（框架）+ `D:\code\konglingfei\bald-admin`（reference example）。
本文件只读代码、只写结论；**未修改任何仓库文件**（唯一写入是本文件自身与 `$env:TEMP` 下的取证产物）。
上游证据：`S1-appkit-surface.md`、`S2-admin-assembly.md`；本文件对二者的关键数字做了独立复核（§0.2）。

---

## 0. 口径、复核与勘误

### 0.1 行号口径
全部 `file:line` 为**真实 UTF-8 行号**。本机 `Get-Content` 会按非 UTF-8 解码这些含中文的源文件并吞行尾 LF，
本文件一律用 `[System.IO.File]::ReadAllText(...).Split([char]10)` 计数、`read` / `grep`(ripgrep) 取行号。
复核：`pkg/appkit/appkit.go` = 772 行（S1 一致）、`pkg/appkit/bootstrap.go` = 1101 行（S1 一致）。

### 0.2 导出符号数复核（对 S1 的勘误）
用 `go doc -all ./pkg/appkit`（go1.27.1）取权威面，机械计数：

| 类别 | 数 |
|---|---|
| `func` / method | **125** |
| `type` | **15** |
| 导出 `const` | **5**（`AuditTypeLog/Store/Stream`、`DefaultHealthPath`、`DefaultReadyPath`） |
| 导出 `var` | **2**（`ErrAlreadyRunning`、`ErrNotRunning`） |
| **合计** | **147** |

S1 报 145（func/method 128 + type 15 + var 2）= 128+15+2+5 = 150，与 go doc 的 125 差 3。
**本文件采用 147 为权威值**（命令可复现），S1 的 145 记为计数通道差异，不影响结论。
后文处置表覆盖全部 147 个符号（§2 分组计数合计 = 147）。

### 0.3 任务书行号勘误（1 处）
任务书指「默认日志后端分派（`bootstrap/bootstrap.go:1047-1088`）」——**该路径不存在**
（`D:\code\konglingfei\bald\bootstrap\bootstrap.go` 不存在；`bootstrap/` 下无 `bootstrap.go`）。
实际位置：**`pkg/appkit/bootstrap.go:1017-1088`**（`resolveLoggerFactory` :1020、`defaultLoggerFactory` :1047）。
本文件按实际路径引用。

### 0.4 「14 组双拼」勘误为 **15 组**
S1 §2.7 列了 14 组，漏了 `AfterStart`(appkit.go:345) ↔ `WithAfterStart`(bootstrap.go:244)。
按 go doc 面穷举，`New` 侧 Option 与 `FromBootstrap` 侧 `With*` 的同义对共 **15 组 / 30 个导出名承载 15 个语义**：

`ID`·`Name`·`Version` 无 `With*` 对；`AfterStop`(appkit.go:351) **无** `WithAfterStop` ——
即「同一个机制在两个入口各导出一次」这件事本身也没做齐（存在非对称），这本身就是 accretive 设计的证据。

### 0.5 契约段消费者复核（**新发现，判据 2 的硬证据**）
`bconf/proto/bootstrap/v1/bootstrap.proto:26-71` 定义 `BootstrapConfig` 共 **15 个顶层段**：
`app` `server` `config` `registry` `logger` `tracer` `metrics` `broker` `storage` `ai` `workflow` `cache` `script` `database` `audit`。

对 `script` 段做定向检索（`ScriptRegistry|WithScriptRegistry|scriptSection|WithScript`，全仓 `*.go`）：
**零命中**；`bootstrap/` 目录下无 `script*.go`（36 个文件全列已核）；`pkg/appkit/` 下无 `script*.go`。
`Script` 相关命中共 261 处，**全部落在 `bconf/gen/go/...` 生成物与无关的 `workflow/argo/options.go:129`**。

**结论：`script` 段已在契约中声明、且**无任何消费者**——`bootstrap/database.go:69-71` 自陈的
「编译期漏加 = 该段配置静默无消费」**不是假设，是已发生的事实**。用户配了 `script:` 段，框架静默忽略、不报错。

---

## 1. 锚点：最小可用装配的目标 `main.go`

### 1.1 现状对照（为什么现在做不到 3 行）
- 今天的最小可用路径是 `FromBootstrap`（`pkg/appkit/bootstrap.go:460`），但它有**唯一必填 Option**
  `WithConfigNamespace`（bootstrap.go:265；缺则 :640-645 直接返回错误），且业务必须自己写两阶段配置预装载
  （`bald-admin/backend/cmd/bald-admin/app/registries.go:99-119`：自建 pflag 扫 `os.Args` 取 `--config` 再 `baldconfig.Load`）。
- 两个 codegen 模板吐的都是**手工路径** `appkit.New(...)`：`internal/codegen/app.go:52`、`internal/codegen/appspec_gen.go:175`。
  即「约定路径」已存在但**生成器仍在生成旧路径**（S1 §5(i).4 的实证）。
- `_example/bald/main.go` 441 行、`bald-admin` 装配 3,787 行（S2 §2）。

### 1.2 目标 `main.go`（A：全新服务，0 → 跑起来）

```go
// cmd/user-service/main.go —— 目标最小可用装配。用户手写，且新增业务域时永不修改。
package main

import (
	"context"

	"github.com/kalandramo/bald/bootstrap/std"
	"github.com/kalandramo/bald/pkg/appkit"
)

func main() {
	appkit.MustServe(context.Background(), std.Standard())
}
```

| 行 | 内容 | 来源 |
|---|---|---|
| 1 | `package main` | 用户提供 |
| 8 | `context` | 标准库 |
| 10 | `bootstrap/std` | **框架默认**（新增包，见 §3.3） |
| 11 | `pkg/appkit` | **框架默认** |
| 14 | `context.Background()` | 用户提供（惯用） |
| 14 | `std.Standard()` | **框架默认**：一个 `appkit.Option`，内含 http/grpc/slog/file+env config/otel tracer+metrics/audit-log 七个**框架自有**后端 provider |
| 14 | `appkit.MustServe` | **框架默认**（新增，唯一入口）：解析 `--config` → 装载契约 → 解析命名空间 → 段驱动装配 → 信号处理 → `Run` → 失败 `os.Exit(1)` |

**用户必须写的行数：3 条语句**（2 个 import + 1 次调用；`func main` 不计）。
**用户 import 的包：2 个**（一个标准库、一个框架包 + 一个 bundle 包）。
**用户需要理解的框架概念：0 个**（无 Option、无注册表、无机制名、无命名空间必填项）。

### 1.3 目标 `main.go`（B：带业务域 —— 生成器输出，接缝验证）

```go
// cmd/bald-admin/main.go —— 由 gen app --spec 生成（AppSpec.domain 驱动），DO NOT EDIT。
package main

import (
	"context"

	"github.com/kalandramo/bald-admin/internal/apiserver/domain/authv1"
	"github.com/kalandramo/bald-admin/internal/apiserver/domain/dictv1"
	// ... 其余域包
	"github.com/kalandramo/bald/bootstrap/std"
	"github.com/kalandramo/bald/pkg/appkit"
)

func main() {
	appkit.MustServe(context.Background(),
		std.Standard(),
		authv1.Domain, dictv1.Domain, // ... 每域一个声明式 Option
	)
}
```

**「生成代码只需 ≤1 次声明式调用即可完成装配」的满足方式**：整个装配是**一次** `appkit.MustServe(...)`；
域是它后面的**声明式参数**（每个域包导出一个 `appkit.Domain` 值），不是选项链、不是注册表构造、不是钩子编号。
生成器只需产出「`std.Standard()` + 域列表」这一个调用点。**我不设计生成器**，只保证该接缝成立。

### 1.4 一个业务域的 3 个文件（判据 1 的落点）

| # | 文件 | 手写/生成 | 内容 |
|---|---|---|---|
| 1 | `api/protos/<domain>/v1/<domain>.proto` | 手写 | 契约（service/message） |
| 2 | `internal/apiserver/biz/<domain>/<domain>.go` | 手写 | 业务逻辑 **+ 同文件内的域清单**（`var Domain = appkit.Domain{...}`） |
| 3 | `internal/apiserver/handler/gin/<domain>.go` | 手写 | 传输适配（REST routes） |

生成器产出（**不算触碰**）：`api/gen/go/<domain>/**`、`openapi.yaml`、`domains_gen.go`（域聚合）、迁移。
**装配文件触碰数 = 0**（今天下界是 4 个，S2 §4.2）。

**诚实前置条件**：判据 1 的「零装配修改」**依赖生成器**（另一设计者交付）。
无生成器时唯一替代是 `biz` 包内 `init()` 自注册，但那与 `pkg/appkit/registry.go:6-8` 明确写下的
「不做 init() 自注册」哲学冲突 → **不选**。这一点是硬依赖，必须写进交付约束。

---

## 2. 147 个导出符号的目标处置表（按组，全量覆盖）

处置词：**保留**（原样）、**取消导出**（改小写）、**下沉子包**（移到 `pkg/appkit/mech` 等）、
**合并**（两个入口合一，删一个名）、**删除**。
判据列：① 3 文件零装配；② 装配行数不随域增长；③ 业务不 import 装配层；④ 30 分钟。

### 2.1 G1 · 入口与生命周期（23：19 func + 3 type + 1 var）

| 符号 | 处置 | 理由 | 判据 |
|---|---|---|---|
| `New` | **合并** → `NewApp(ctx, opts ...Option) (*App, error)` | 单入口的一半 | ④ |
| `FromBootstrap` | **删除** | 454 行函数体整体变为 `Serve` 内部实现；第二入口是 15 组双拼的根因 | ④ |
| `BootstrapOption` | **删除** | 与 `Option` 合并为**唯一** Option 类型 | ④ |
| `Option` | **保留**(改形) | `func(*AppKit)` → `interface{ Apply(*Decl) }`（见 §5，使 `mech` 可跨包构造） | ①②④ |
| `Run` | **保留** | 阻塞式运行，e2e 与 `MustServe` 共用 | ④ |
| `Done` / `Err` | **保留** | 嵌入式/测试观察，无替代 | ④ |
| `ID` `Name` `Version` | **保留** | 元数据；`Serve` 路径下从契约 `app` 段取（bootstrap.go:648-651 已是此语义） | ④ |
| `StopTimeout` | **保留** | 停机超时 | ④ |
| `Servers` / `WithExtraServers` / `WithExtraServerFunc` | **合并**(3→1) + **取消导出** `WithExtraServerFunc` | 三者都是「加一个 server」，差异只因「构造期拿不到运行期资源」（bootstrap.go:229-241 注释自陈）。而该根因**已修**：server 构造已延后到 Run 期（bootstrap.go:533-543 + `wiring_order_test.go:27-65`）→ 逃生舱无存在理由（S1 §5(i).5） | ④ |
| `Registrar` / `WithRegistrar` | **合并** → `Registrar` | 15 组双拼之一 | ④ |
| `BeforeStart` / `WithBeforeStart` | **合并** → `BeforeStart` | 同上 | ④ |
| `BeforeStop` / `WithBeforeStop` | **合并** → `BeforeStop` | 同上 | ④ |
| `AfterStart` / `WithAfterStart` | **合并** → `AfterStart` | 同上（S1 漏此组） | ④ |
| `AfterStop` | **保留** | 非对称项；合并后补齐 `AfterStop` 语义一致 | ④ |
| `ErrAlreadyRunning` | **保留** | `Run` 防重入 | ④ |
| `AppKit` | **保留**(更名 `App`) | 与 `appkit` 包名重复的 `AppKit`/`appkit` 二义；改名是纯机械替换 | ④ |

G1 小计：**23**。净减：`FromBootstrap` `BootstrapOption` `WithRegistrar` `WithBeforeStart` `WithBeforeStop`
`WithAfterStart` `WithExtraServers`(并) `WithExtraServerFunc`(并) = 8 名消失。

### 2.2 G2 · 配置（19：18 func + 1 type）

| 符号 | 处置 | 理由 | 判据 |
|---|---|---|---|
| `WithConfigNamespace` | **删除** | **唯一必填 Option**（bootstrap.go:265，缺则 :640-645 报错）。根因是「env 前缀由配置命名空间派生」而命名空间又可能来自配置（自指，appkit.go:56-66 详解）。修法见 §3.4：**两遍合并**（先文件层 → 解析 app.name → 再按派生前缀合并 env 层），自指消失，用户 env 名不变 | ①④ |
| `ConfigNamespace` | **取消导出** | 退为 `Serve` 内部状态；`NewApp` 路径按同上规则推导 | ④ |
| `ConfigFile` / `WithConfigFile` | **合并** → `ConfigFile` | 双拼 | ④ |
| `WatchConfigFile` / `WithWatchConfig` | **合并** → `WatchConfigFile` | 双拼；且改名消歧（`WatchConfig` vs `WatchConfigFile`） | ④ |
| `RemoteConfig` / `WithRemoteConfig` | **合并** → `RemoteConfig` | 双拼 | ④ |
| `StrictContract` / `WithStrictContract` | **合并** → `StrictContract` | 双拼 | ④ |
| `Bind` / `WithBind` | **合并** → `Bind` | 双拼。**同时降级**：见 §2.2 注 | ④ |
| `ConfigLayers` `Env` `OnConfigChange` | **保留** | 无对应项、语义独立 | ④ |
| `PlainBinder` | **取消导出** | `Bind(prefix, opt any)` 的内部协议（appkit.go:187）；改用公开接口 `interface{ Flags(*pflag.FlagSet) }` 并**取消用户可感知的「无前缀 flag」特例** | ④ |
| `ParseStringList` | **下沉** → `bootstrap/config` | 配置值解析属装载层（reconcile.go:326-361），非 App 编排 | ②④ |
| `AppKit.Config` | **删除** | 返回装载器具体类型 `*config.Store`（appkit.go:459），把实现泄露进 App 面；`Settings()` 已给只读快照 | ③④ |
| `AppKit.Settings` | **保留** | 业务读配置快照的唯一正门 | ③ |

**注（`Bind` 的降级路径）**：`Bind` 的注释（appkit.go:191-223）要求用户理解「flag 必须进 AppKit 的
FlagSet 才压得过 env/文件」这条装载器内部规则。目标形态：`appkit.Option` 之一 `appkit.BindFlags(prefix string, spec any)`
—— 由框架从**结构体标签**推导 flag 名与键路径，用户不再持有 `FlagSet`。代价：一次 breaking（签名变），
收益：判据 ④ 的「0 概念」达成。**若保守，可先只做双拼合并、保留现签名。**

G2 小计：**19**。消失 6 名（`WithConfigNamespace` 已算 G2；`WithConfigFile` `WithWatchConfig` `WithRemoteConfig`
`WithStrictContract` `WithBind`），另 2 名下沉/删除（`ParseStringList`、`AppKit.Config`），1 名取消导出（`PlainBinder`）。

### 2.3 G3 · 协议面与健康检查（14：11 func + 1 type + 2 const）

| 符号 | 处置 | 理由 | 判据 |
|---|---|---|---|
| `WithHTTP` `WithGRPC` `WithGatewayRegister` | **保留**（改名去掉 `With` 前缀：`HTTP` `GRPC` `GatewayRegister`） | 协议面是真实业务决策，且 http/grpc provider 框架自注册（bootstrap.go:568-588，`Has` 判重） | ④ |
| `WithGRPCOptions` | **取消导出** | 逃生舱；根因（构造期拿不到运行期资源）已修（同 G1 的 `WithExtraServerFunc`）。保留内部字段 `grpcOptionsFn`（bootstrap.go:72）即可，不必导出 | ④ |
| `WithServerRegistry` | **删除** | 被 §3 的单一 provider 注册取代 | ② |
| `WithHealth` `WithProbePaths` `WithHealthPollInterval` `WithProbes` | **合并/取消导出** | `HealthOption` 是第二套 Option 类型，只服务一个能力。目标：`Health(...)` + `ProbePaths(...)` 收进 `Option`；`WithProbes` 保留（它是 `http.Handler` 包装，供 provider 用） | ④ |
| `HealthOption` | **删除** | 第三套 Option 类型，合并进 `Option` | ④ |
| `DefaultHealthPath` `DefaultReadyPath` | **保留** | 常量，零成本 | ④ |

G3 小计：**14**。

### 2.4 G4 · 资源域注册表与访问器（**45**：24 func + 6 type + 3 const + 12 accessor）

这是收敛的主战场。**45 个符号里，目标是只剩 3 个。**

| 符号 | 处置 | 理由 | 判据 |
|---|---|---|---|
| 12 × `With*Registry`（`WithConfigRegistry` :276、`WithRegistrarRegistry` :212、`WithLogRegistry` :329、`WithDatabaseRegistry` database.go:34、`WithCacheRegistry` cache.go:33、`WithStorageRegistry` storage.go:34、`WithAiRegistry` ai.go:37、`WithWorkflowRegistry` workflow.go:33、`WithBrokerRegistry` broker.go:36、`WithTracerRegistry` :337、`WithMetricsRegistry` :345、`WithAuditRegistry` :355） | **删除**（12） | 被单一 `Provider` 注册取代（§3）。每新增资源域今天要同步改 **7 处**（S1 §3.3：registry 文件 / `bootstrapSpec` 字段 / `With*Registry` / `build*` / 阶段 B 调用 / `Effect` 注册 / AppKit 字段+访问器对）→ 判据 ② 的直接违反 | ②④ |
| 13 × `XRegistry` struct 类型（10 in `bootstrap/` + `TracerRegistry` obs.go:63、`MetricsRegistry` :132、`AuditRegistry` audit_registry.go:81） | **合并**为 1 个泛型/统一实现 | 13 份 `mu + providers map + Register/MustRegister/Build` 逐行同构（S1 §3.1）；且 `appkit.Registry[T]`（registry.go:18）已存在却**全仓零引用** | ② |
| 3 × `New*Registry`（`NewTracerRegistry` :69、`NewMetricsRegistry` :138、`NewAuditRegistry` audit_registry.go:87）+ 10 个 `bootstrap.New*Registry` | **删除**（13） | 用户不再构造注册表 | ②④ |
| 39 × `Register` / `MustRegister` / `Build` 方法（13 × 3） | **取消导出** → 收归框架内部 | provider 注册由 `std.Standard()` / 用户 `Provider` 声明完成；`Build` 是框架段驱动循环的内部步骤 | ② |
| 3 × `AuditType*` const | **下沉** → `pkg/audit` | 后端类型常量属能力包 | ② |
| 6 × `*Provider` 类型（`AuditProvider` :76、`TracerProvider` :59、`MetricsProvider` :127；database/cache/storage/ai/workflow/broker 的 provider 类型在 `bootstrap/`） | **合并** 1 个 `appkit.Provider` 接口 | 三个函数类型各自的签名差异（返回 `audit.Auditor` / `func()` / `http.Handler`）正是「按域胶水」的来源 | ② |
| 12 × `AppKit.{Ai,Ais,Broker,Brokers,Cache,Caches,Database,Databases,Storage,Storages,Workflow,Workflows}` | **合并** → 1 个 `App.Resource(segment string) (any, bool)` | 6 域 × 2 个返回 `any` 的访问器（S1 §2.5、appkit.go:77-97 六个 map 字段）；类型安全靠消费侧断言（database.go:62 注释自陈） | ②③ |

**G4 目标面 = 3 个**：`appkit.Provider`（接口）、`appkit.Providers(...)`（声明式 Option）、`App.Resource(segment)`（取用）。
`AppKit` 的 6 个 `map[string]any` 字段（appkit.go:77-97）合并为 1 个 `resources map[string]any`。

**「返回 `any`」这个泄漏要不要治？** 治不了、也不该在这里治：客户端类型异构是装配层的必然
（`bootstrap/database.go:28-31` 已论证）。目标只是把 **12 个** `any` 出口收成 **1 个**，
让「类型安全在消费侧恢复」这条规则只出现一次而不是十二次。

### 2.5 G5 · 机制层（26：17 func + 6 method + 3 type）→ **全部下沉 `pkg/appkit/mech`**

`capability.go`(95) + `component.go`(116) + `effect.go`(92) + `keywatch.go`(91) + `mount.go`(194) +
`reconcile.go`(367) = **955 行 = appkit 非测试代码的 23.6%**（S1 §4，已复核）。

| 符号 | 处置 | 理由 |
|---|---|---|
| `Effect` `EffectTimeout` `WithEffect` | **下沉 + 合并** → `mech.Effect` | T1 效应账本：普通用户不需要 |
| `Provides` `Requires` `WithProvides` `WithRequires` | **下沉 + 合并** → `mech.Provides` `mech.Requires` | S1 §6 判定 fail-fast 能力声明是「纯收益」（capability.go:45-94），**保留能力**、只改可见性 |
| `Component` `ComponentFunc` `Components` `ComponentTimeout` `WithComponents` | **下沉 + 合并** → `mech.Component*` | C1 生命周期 |
| `OnKeyChange` `WithOnKeyChange` | **下沉 + 合并** → `mech.OnKeyChange` | R1 key 级订阅 |
| `Reconcile` `ReconcileFunc` `WithReconcile` `NewReconcileCtx` | **下沉 + 合并** | R1-2 期望态协调；`NewReconcileCtx` 注释自陈「供测试或手动触发」（reconcile.go:157-159）→ 测试用 |
| `ReconcileCtx` + `String` `StringSlice` `Mounted` `Has` `Mount` `Unmount` | **下沉** | 把 K8s controller 的期望态/实际态模型塞进 App 层（S1 §2.1） |
| `DiffStrings` | **下沉** → `mech`（**必须与 codegen 同批发**） | 生成物直接调用它：`internal/codegen/appspec_gen.go:232` `appkit.DiffStrings(want, rctx.Mounted())` |

G5 小计：**26**。appkit 包内这 26 个名字全部消失，改为 `import ".../pkg/appkit/mech"` 才可见。
判据 ②（appkit 面不再被机制撑大）、④（默认面 0 概念）。

### 2.6 G6 · 运行期挂载 / 测试装置 / 泛型注册表（17：15 func + 1 type + 1 var）

| 符号 | 处置 | 理由 | 判据 |
|---|---|---|---|
| `Registry[T]` + `NewRegistry` + `Register` `MustRegister` `Get` `List` | **保留但下沉** → `mech`；且**必须先有消费者** | 全仓零引用（含测试/示例/cmd）——是「无消费者的导出机器」（S1 §2.1）。目标状态：它是 §3 单一 provider 注册表的**底层实现**，从死机器变成唯一实现 | ② |
| `Registry[T].Mount` `Unmount` | **下沉** → `mech` | A1 运行期可逆挂载，仅管理面热插拔用 | ②④ |
| `AppKit.MountComponent` `UnmountComponent` `ListComponents` | **下沉** → `mech`（改为 `mech.Mount(app, ...)`） | 同上；S1 判为「管理面」泄漏 | ② |
| `AppKit.ReconcileNow` `UndoEffects` `Resolve` | **取消导出** | 三者注释均自陈「供测试/手动触发」（reconcile.go:175、effect.go:71-74、capability.go:50-51），`Run` 已自动调用 → 测试用 `export_test.go` 或 `mech` | ② |
| `AppKit.SetRegistrar` | **删除** | 注释自陈只服务 `New` 路径的时序补丁（appkit.go:268-272）；单入口后消除 | ④ |
| `NewHarness` | **下沉** → `mech`/`appkittest` | harness.go:3-6 自陈「生产代码不应使用」，为跨 module 集成测试而导出（仓库 93 个 go.mod 是根因） | ② |
| `ErrNotRunning` | **保留** | 运行期挂载的错误语义 | ④ |

G6 小计：**17**。

### 2.7 G7 · 可观测性与审计的后端装配（3：`Auditor`、`StartMetricsServer`、`WithLogDecorators`）

| 符号 | 处置 | 理由 | 判据 |
|---|---|---|---|
| `StartMetricsServer` | **下沉** → `observability/metricshttp`（新包） | **App 层自己 `ListenAndServe`**（observability.go:202-218，goroutine 在 :212-216）——真起监听面是能力构造，不是 App 编排 | ② |
| `WithLogDecorators` | **取消导出** | 参数类型是 `bslog.Option`（bootstrap.go:308）→ 把具体日志后端类型泄露进 Option 面；装饰应由 provider 自持 | ③④ |
| `Auditor` | **取消导出** | appkit 自身 mount/reconcile 审计事件的 sink 注入（mount.go:166-171）；目标改由 `mech` + 显式注入 | ② |

G7 小计：**3**。`TracerProvider`/`MetricsProvider`/`AuditRegistry` 等已在 G4 计过，不重复。

### 2.8 处置汇总

| 组 | 计 | 处置后 appkit 包内可见 | 说明 |
|---|---|---|---|
| G1 入口/生命周期 | 23 | ~12 | 单入口、单 Option、8 名消失 |
| G2 配置 | 19 | ~9 | 必填项消失、6 名合并、2 名下沉/删除 |
| G3 协议/健康 | 14 | ~8 | 3 名合并、第三套 Option 类型消失 |
| G4 资源域注册表 | 45 | **3** | 12+13+13+39 名收归内部 |
| G5 机制层 | 26 | **0** | 全部下沉 `mech` |
| G6 挂载/测试/泛型注册表 | 17 | ~2 | 下沉 `mech` |
| G7 可观测/审计装配 | 3 | 0 | 下沉能力包 |
| **合计** | **147** | **≈34** | appkit 默认可见面 **147 → ~34（-77%）** |

---

## 3. 注册表收敛方案

### 3.1 现状（可核验的形状）
13 张注册表，统一形状 `type XProvider func(...)` + `type XRegistry struct{ mu; providers map[string]XProvider }`
+ `NewXRegistry` + `Register` + `MustRegister` + `Build`。
用户最小成本 = **3 行/注册表**（`New` + `MustRegister` + `With*Registry`），13 域全用 ≈ 39 行（S1 §3.1）。

**两处手工枚举表**（判据 ② 的硬伤）：
- `bootstrap/database.go:77-86` `databaseSections`：8 个引擎段，注释 :69-71 自陈
  「新引擎在 bconf 加段后**在此追加**（编译期漏加 = 该段配置静默无消费）」。
- `bootstrap/server.go:100-141` `serverSections`：**32 个**段，只有 `http`/`grpc` 是 `implemented:true`，其余 30 段 `false`。

**并且该风险已经实现**：`script` 段在 `BootstrapConfig` 中声明（bootstrap.proto:64，字段 13），
**全仓零消费者**（§0.5 复核）→ 配了静默无效。这是方案选择的第一约束。

### 3.2 候选 A：段驱动单一 Provider 注册（**选定**）

```go
// pkg/appkit —— 唯一注册抽象
type Provider interface {
	// Segments 声明本 provider 消费的契约段路径（如 "server.http"、"database.sql"、"logger"）。
	Segments() []string
	// Build 在段存在时构建；段缺失返回 (nil, nil, nil) 表示跳过。
	Build(ctx context.Context, cfg *bootstrapv1.BootstrapConfig, app *App) (any, Cleanup, error)
}

// 声明式注册（1 个 Option，可复数）。注意 Go 里类型与函数不能同名，
// 故接口叫 Provider、构造器叫 Providers。
func Providers(ps ...Provider) Option
```

框架段驱动循环（伪码，替换 `FromBootstrap` 的 12 段手工调用）：
```go
present := contractSegments(cfg)          // 由 protobuf 反射枚举，不手写
for _, p := range registeredProviders {
	if !anyPresent(present, p.Segments()) { continue }   // 段缺失 → 跳过
	...Build...
}
if missing := present.Minus(covered); len(missing) > 0 {  // ← 新增的 fail-fast
	return fmt.Errorf("contract declares segments with no provider: %v", missing)
}
```

**三个机制要点**
1. **枚举表消失**：段名由 provider **自持**（`Segments()`），存在性判定由 `cfg` 的
   protobuf 反射给出——`BootstrapConfig` 具备完整反射能力（`bootstrap.pb.go:76`
   `ProtoReflect() protoreflect.Message`、`:198` `File_..._proto`、`:297` `protoimpl.TypeBuilder`），
   所以 `databaseSections`(:77-86) 与 `serverSections`(:100-141) 两张手工表**都可删除**。
2. **fail-fast 不但保住，还加强了**（见 §3.5）。
3. **每新增资源域 = 1 个 provider 文件 + 1 行注册**，框架内 7 处同步点（S1 §3.3）只剩 1 处。
   且这个「1 处」是**用户/contrib 侧**，不是核心装配代码 → 判据 ② 成立。

### 3.3 `bootstrap/std`：框架自有后端的唯一 import 点
`std.Standard()` 返回 1 个 `Option`，内含框架**自有**后端：
`server.http`、`server.grpc`、`logger`(slog)、`config`(file+env)、`tracer`+`metrics`(otel)、`audit.log`。
第三方后端（nacos/etcd/loki/minio/asynq/gateway/…）仍走各自的 `*/contract` 包显式 import。
**保留了原哲学的核心收益**（`registry.go:6-8`：依赖图可见、未 import 的后端零编译成本），
只是把「13 个 registry 构造 + 39 行 MustRegister」换成 1 个 bundle Option。

### 3.4 候选 B：13 份变薄壳 over `Registry[T]`
把 `appkit.Registry[T]`（registry.go:18）升为唯一实现，13 个 `XRegistry` 变成
`type DatabaseRegistry struct{ *Registry[DatabaseProvider] }` 之类的薄壳（每份 ~70 行 → ~15 行）。

- **收益**：13 × ~60 行样板 ≈ **-700 行**框架代码；`Registry[T]` 从死机器变成有消费者。
- **不解决**：① 7 处同步点全在（`bootstrapSpec` 字段、`With*`、`build*`、阶段 B 调用、`Effect` 注册、
  AppKit 字段、访问器）；② 两张手工段表还在；③ 用户侧仍是 39 行 `New`+`MustRegister`；
  ④ `script` 静默漏消费**依然检测不到**。
- 结论：**B 是 A 的真子集**（A 内部就用 `Registry[T]` 承载 provider 表）。

### 3.5 选定、代价与 fail-fast 如何保住

**选定 A**（B 作为 A 的落地步骤之一：先把 13 份同构收成 `Registry[T]`，再换成段驱动循环，可分两批发布）。

**代价（诚实列出）**
1. **12 个 `With*Registry` 名字消失** → 所有用户装配代码 breaking（含 bald-admin）。迁移见 §6。
2. `Build` 签名统一后，provider 返回 `any` 的粒度从「按域」变为「按段」——`database.sql`、`database.mongodb`
   各是一个段名，所以「8 个引擎段可并存」这个语义（`bootstrap/database.go:24-26`）**必须保住**：
   provider 的 `Segments()` 返回 8 个段名、`Build` 逐段构造。这是必须写进实现的约束，否则回归。
3. 反射枚举依赖 `bconf` 生成物保留 protobuf-go 运行时（已是事实，`bootstrap.pb.go` 已 import `protoreflect`），
   **不新增依赖**。
4. 段名成了**新的手写字符串**——但它**不会静默失败**：段名拼错时该 provider 永不触发，
   而「契约里存在、无人消费」的检查会把它报出来（下条）。这是与今天手工表的本质差别。

**fail-fast 三层保住 + 加强**
| 层 | 今天 | 目标 |
|---|---|---|
| L1 段存在但 provider 未注册 | 报错（database.go:124、cache.go:115、storage.go:109、ai.go:114、workflow.go:131、broker.go:131、registrar.go:111、audit_registry.go:167、bootstrap.go:1066） | **同语义保留**，错误信息改为列出「已注册 provider 及其段」 |
| L2 provider 注册但段缺失 | 跳过（合法：`server.go:146-148` 论证 nil=跳过） | **同语义保留** |
| L3 段存在且**无人实现**（框架+用户都没有） | **静默**（`script` 实证） | **新增：启动报错**，列出未覆盖段 + 提示「引入实现包或从契约删除该段」 |
| L4 段名拼错 / provider 段名与契约不一致 | 无检测 | L3 覆盖（因为拼错 ⇒ 该段无人覆盖） |

**唯一需要取舍的**：契约允许声明「框架尚未支持」的段（`server` 的 30 个未实现段、`script`）。
L3 若一律报错，会让「契约先行」的工作流变难。折中：**默认 `StrictSegments(true)` 报错**，
`WithUnimplementedSegments("server.http3", …)` 显式白名单跳过；缺省行为是**报错**（fail-closed）。

---

## 4. 职责边界的落点（5 项）

### 4.1 真起监听面 —— `StartMetricsServer`（observability.go:202-218）
- **现状证据**：函数体自建 `http.NewServeMux`(:209)、`http.Server`(:211)、`go srv.ListenAndServe()`(:212-216)，
  返回 `*http.Server` 供停机。这是**能力构造**（起监听面）而不编排。
- **新归属**：`observability/metricshttp`（新公开包）持有 `ListenAndServe`；appkit 只把
  `MetricsProvider`（observability.go:127）返回的 `http.Handler` 交给它，并把 `io.Closer` 挂进停机 Effect。
- **迁移动作**：`MetricsProvider` 签名从「返回 `http.Handler`」变为「返回 `Listener`/`io.Closer`」，
  即让**后端**负责监听；受影响只有 `contrib/observability-otlp`（1 个 module）与其测试。
- **判定：改。** 代价小、边界清晰。

### 4.2 组装 `MultiAuditor` + 逆序回滚 + 改全局 `SetAuditor`（audit_registry.go:148-197、240-241）
- **现状证据**：`Build` 内自己组装 `audit.NewMultiAuditor(auditors...)`(:184)、自写逆序回滚闭包(:148-152、:188-196)、
  并在 :240-241 改**全局** `SetAuditor`（`mount.go:170` `resolveAuditor` 读全局兜底）。
- **新归属**：拆两半——
  (a) 「多后端组装 + 逆序回滚」是**审计能力自己的语义** → 移到 `pkg/audit`
      （`audit.Compose(backends ...Backend) (Auditor, Cleanup, error)`）。
  (b) **全局 `SetAuditor` 删除** → 审计器改为显式注入（传给 bundle / transport / audit-aware 组件）。
- **迁移动作**：`pkg/audit` 新增 `Compose`；appkit 的 `buildAudit` 只做「段 → provider 列表 → 调 Compose →
  把 Auditor 交给依赖方」。全局状态消费者（`audit.GetAuditor()`）需改注入。
- **判定：改。** 全局可变状态是「同一进程两套 e2e 互相污染」的根因（`internal/codegen/app.go:70-72` 注释
  已把 `RegisterTenant` 类的全局写入描述为需要 Effect 对冲的负担）；能力自己的组装逻辑也不该住 App 层。

### 4.3 配置点路径解析 / 标量字符串化 / 列表解析（reconcile.go:94-132、326-361）
- **现状证据**：`settingAtPath`(:94-114) 按 `"."` 拆点路径、`settingString`(:118-132) 标量转字符串、
  `ParseStringList`(:326-361) 解析 `string`/`[]string`/`[]any`/逗号分隔——三者是**配置装载层的值语义**，
  却长在 `reconcile.go`（R1-2 协调器文件）里；`DiffStrings`(:300) 是集合工具。
- **新归属**：`bootstrap/config`（配置仓库实现包）导出 `config.At(m, "a.b.c")` / `config.String(v)` / `config.StringList(v)`。
  `DiffStrings` 移入 `mech`（因为它是协调器的配套，且 codegen 在用）。
- **迁移动作**：appkit 的 `ReconcileCtx.String/StringSlice`(:75/:85) 改为薄转发；
  `ParseStringList` 从 appkit 导出面移除（用户改用 `config.StringList`）。
  **`DiffStrings` 必须与 codegen 同批发**（生成物直接调用：`appspec_gen.go:232`）。
- **判定：改（下沉）。** 直接服务判据 ②（reconcile.go 367 行里约 90 行是配置工具）。

### 4.4 认识业务身份 `authn.AuthClaimsFromContext`（mount.go:183、reconcile.go:278）
- **现状证据**：appkit 为发审计事件而 import `pkg/authn`（mount.go:35、reconcile.go:48），
  并在 `auditComponent`(:183-185) 与协调审计里读 `claims.Subject`/`claims.TenantID`。
  即：**App 层必须知道「业务身份从 ctx 里怎么取」**。
- **新归属**：依赖倒置——appkit 定义一个它需要的**最小端口**：
  ```go
  // pkg/appkit
  type IdentityFunc func(ctx context.Context) (subject, tenant string)
  func Identity(fn IdentityFunc) Option   // 缺省 = 返回空串（审计事件 Subject/Tenant 留空）
  ```
  `authn` 到端口的适配由**应用或 contrib** 提供（1 行 lambda：`appkit.Identity(func(ctx) {...authn...})`）。
- **迁移动作**：删除 appkit 的 `pkg/authn` import（2 处）；`bald-admin` 在装配处补 1 行 `appkit.Identity(...)`；
  默认行为回归为「空身份」（今天 claims 为 nil 时也是空串，mount.go:182-185）→ **语义无变化**。
- **判定：改。** 改动极小（~10 行），收益是 App 层不再认识业务身份概念（判据 ③ 的精神）。

### 4.5 默认日志后端分派 4 分支（**pkg/appkit/bootstrap.go:1017-1088**）
- **现状证据**：`resolveLoggerFactory`(:1020-1033) 两级分发 + `defaultLoggerFactory`(:1047-1087)
  三分支：阶段 A 回退 bslog(:1050-1051)、`backends` 项 `type=="slog"` 直构 + 非 slog fail-fast 并打印
  「请显式注册」的教学错误(:1060-1070)、空 `backends` fail-fast(:1054-1055)，出口包 `log.NewFilterLogger`(:1085)。
  同时 appkit import 具体后端 `log/bslog`（bootstrap.go:52）。
- **新归属**：**bslog 就是 `logger` 段的一个普通 provider**（`bootstrap/log.go` 已有
  `LogRegistry`(:29)/`BslogLoggerProvider`(:170) 这套形状），把默认路径删除、只留 provider。
- **迁移动作**：`resolveLoggerFactory`/`defaultLoggerFactory` 删除；`slog` provider 注册进 `std.Standard()`；
  `WithLogDecorators`（`bslog.Option`，bootstrap.go:308）取消导出，装饰改为 provider 自持。
  **必须保住**：阶段 A（契约装载前）日志可见 —— 改为框架持有**一个引导 logger**（不属于任何后端），
  阶段 B 用 provider 结果**替换**它；这保留「启动期日志不丢」的既有保证（`defaultLoggerFactory:1050-1051` 的初衷）。
- **判定：改。** 直接服务判据 ②（appkit 去掉 72 行能力分派 + 1 个具体后端依赖）。

### 4.6 「不改」清单（有意取舍，明确不改）
| 项 | 理由 | 代价 |
|---|---|---|
| `stopAll` 五阶段 + `Stop` 传 `context.Background()`（appkit.go:621-666） | go-lulu 既有防坑（doc.go:35 BUG-1），收益明确 | 无 |
| 显式注册、不用 `init()` 自注册（registrar.go:13-15、registry.go:6-8） | 依赖图可见、未 import 后端零编译成本 | `std` bundle 折中（§3.3） |
| 能力声明 fail-fast（capability.go:45-94） | 把 nil panic 提前为启动期错误，纯收益 | 95 行（下沉 `mech` 后不再占默认面） |
| `Ais()`/`Brokers()` 等返回副本 | 防外部改写内部 map（ai.go:72-77） | 合并为 `Resource` 后**必须保留副本语义** |
| 前端审核不阻断（mount.go:176-193，有 `recover` + 仅记日志） | 与「审计不能拖垮业务」纪律一致 | 无 |
| `FromBootstrap` 单函数 454 行「单文件可读」 | 属取舍非缺陷；但**函数本身要被拆**（因入口合并） | — |

---

## 5. 机制层的显隐策略

### 5.1 目标分层
| 层 | 面 | 内容 |
|---|---|---|
| **默认面** `pkg/appkit` | ~34 个符号 | `Serve`/`NewApp`/`Option`/`Decl`/`Provider`/`Providers`/`Bind`/`ConfigFile`/`HTTP`/`GRPC`/`Health`/`StrictContract`/`Resource`/`Identity`… |
| **高级面** `pkg/appkit/mech` | 955 行机制 + `Registry[T]` + `NewHarness` | `Effect` `Provides` `Requires` `Component` `ComponentFunc` `OnKeyChange` `Reconcile` `ReconcileCtx` `DiffStrings` `Registry[T]` `Mount/Unmount` |
| **能力面** `bootstrap/std` + `*/contract` | provider | 后端实现 |

### 5.2 跨包构造的技术方案（这是本设计里唯一有 Go 语言约束的地方）
`appkit.Option` 今天是 `func(*AppKit)`（appkit.go:249），**包外无法构造**（字段未导出）。
若 `mech` 要返回 `appkit.Option`，必须让 Option 可跨包实现。方案：

```go
// pkg/appkit
type Decl struct {                       // 纯数据，全导出字段
	Effects    []EffectDecl
	Reconciles []ReconcileDecl
	KeyWatches []KeyWatchDecl
	Provides   []string
	Requires   []RequireDecl
	Components []ComponentDecl
}
type Option interface{ Apply(*Decl) }    // 公开方法 → mech 可实现
```
- `mech.Effect(...) mech.Option`，`mech.Option` 实现 `appkit.Option`（只需 `Apply(*appkit.Decl)`）。
- `appkit.Serve(ctx, opts ...Option)`；`mech` **单向** import `appkit`，appkit **不** import `mech`。
- **实测兼容性**：`bald-admin` 用的是 `capOpts := []appkit.Option{...}` + `append`（assembly.go:309-583），
  `app.Option` 从 func 类型变接口后**这些代码照常编译**（`[]Option` 存接口值）；
  唯一破坏是任何对 `Option` 做**函数调用**或需 `func` 类型的代码（全仓检索未发现此类用法）。

### 5.3 四条隐藏手段的取舍
| 手段 | 选择 | 理由与代价 |
|---|---|---|
| 子包（`pkg/appkit/mech`） | ✅ **选** | 编译期真隐藏、文档分层自然；代价：跨包构造需 `Option` 改接口（§5.2），已核兼容 |
| 可选接口（用户实现 `Component`/`Provider`） | ✅ 选（作为机制入口） | 无需子包也能扩展；但**不能**隐藏机制名，故只用于 `Provider`/`Domain` 这类**该被用户实现**的契约 |
| 构建标签（`//go:build appkit_mech`） | ❌ 不选 | 同一 module 内 build tag 会让 `go vet`/测试矩阵分裂、IDE 体验差；且隐藏效果不如子包（代码仍在包的文档面里） |
| 仅文档分层（同名包内靠注释分区） | ❌ 不选 | 今天就是这个状态（`appkit` 一个包里 147 个导出名），已被证明失效 |
| 每机制一个子包（`appkit/effect`、`appkit/reconcile`…） | ❌ 不选 | 6 个子包各自要暴露 Option 构造，`Option` 得实现 6 次；收成 1 个 `mech` 更好 |

### 5.4 高级用户如何按需启用（目标形态，1 行）
```go
import "github.com/kalandramo/bald/pkg/appkit/mech"

appkit.MustServe(ctx, std.Standard(),
    mech.Provides("db"),
    mech.Requires("audit.store", "db"),
    mech.Effect("tenant-registration", unregisterTenant),
    mech.Components(mech.ComponentFunc("trace.provider", shutdownTracer)),
    mech.Reconcile("audit.backends", reconcileAudit),
)
```
默认面**不含**这些名字；`go doc pkg/appkit` 不再出现 `Reconcile`/`Effect`/`Component`。

---

## 6. 兼容与迁移清单

### 6.1 Breaking 项逐条
| # | Breaking | 影响面 | 用户要改什么 | 批次 |
|---|---|---|---|---|
| B1 | `FromBootstrap` 删除、`New`→`NewApp`、`BootstrapOption` 删除、`Option` 变接口 | 框架内 + bald-admin + **生成器模板** | 装配入口改为 `Serve`/`NewApp`；`appkit.Option` 切片可保留 | 2 |
| B2 | 15 组双拼合并（删 15 个 `With*` 或 15 个裸名） | 同上 | 全仓机械替换（`WithBind`→`Bind` 等） | 2 |
| B3 | 12 个 `With*Registry` 删除 → `Provider` 声明 | 框架内 + bald-admin + contrib | **最大改动**：13 张注册表构造 + `MustRegister` 全部删除，改 `std.Standard()` + 按需 `appkit.Provider(...)` | 2 |
| B4 | 12 个 `AppKit.X()/Xs()` → `Resource(segment)` | bald-admin | `app.Database("sql")` → `app.Resource("database.sql")` | 2 |
| B5 | 机制 26 名下沉 `mech`（G5）+ 挂载/测试 17 名（G6） | 框架内 + bald-admin + **生成器** | `import mech`；`appkit.DiffStrings`→`mech.DiffStrings`（生成物 `appspec_gen.go:232` 必须同批改） | 2 |
| B6 | `WithConfigNamespace` 删除（两遍合并修自指） | 框架内 + bald-admin | 删掉该 Option；**env 名不变**（这是选两遍合并而非改前缀的理由） | 3 |
| B7 | `AppKit.Config()` 删除；`StartMetricsServer` 下沉；`Auditor`/`WithLogDecorators` 取消导出 | 框架内 + contrib-otlp | `Settings()` 替代 `Config()`；metrics 监听交回 provider | 3 |
| B8 | 全局 `SetAuditor` 删除、审计组装移入 `pkg/audit.Compose` | 框架内 + bald-admin | 审计器显式注入（不再读全局兜底） | 1 |
| B9 | `AppKit` 类型更名 `App` | 全部 | 机械替换 | 2 |
| B10 | L3 新增「无人消费段」fail-fast 默认开启 | 框架内 + 用户契约 | 契约里若有 `script` 等未实现段，需从契约删除或加白名单 | 1 |

### 6.2 建议发布顺序（哪些必须同批）
- **批次 1（可先发，零用户破坏）**：13 份注册表内部收成 `Registry[T]`（候选 B 作为步骤）；
  `pkg/audit.Compose` 引入并让 appkit 调它（B8 的内部化）；L3 段覆盖检查（**先做成告警**，
  这样 `script` 静默漏消费立刻可见而不破坏构建）；`pkg/appkit/mech` 子包**建立 + 原名保留为 alias**。
  → 批 1 结束时可核验：`script` 段被报出来、13 份样板代码收敛、机制可 import 但旧名未删。
- **批次 2（必须同批，原子）**：B1+B2+B3+B4+B5+B9。
  理由：入口合并、双拼合并、注册表收敛、机制下沉**共享同一个 `Option` 类型改动**（§5.2），
  拆开发会出现「两次 Option 类型迁移」；且 codegen 两个模板（`internal/codegen/app.go:52`、
  `appspec_gen.go:115-175`）必须在同一批发新（否则生成的代码编译不过）。
  同批交付物：框架 + 两个 codegen 模板 + bald-admin 迁移 + contrib 各 module 的 `Provider` 化。
- **批次 3**：B6+B7 + `Domain` 一等概念（判据 1 的前置，依赖另一设计者的生成器骨架）。
  B6 必须与 `Domain` 分开评估：env 前缀解析是纯框架内改动，先单独可测。

### 6.3 迁移动作清单（给用户）
1. `main.go`：`appkit.FromBootstrap(cfg, opts...)` → `appkit.MustServe(ctx, std.Standard(), ...)`。
2. 删除全部 `NewXRegistry()` / `MustRegister` / `WithXRegistry`（13 张表）→ 换 `std.Standard()` + 按需 `appkit.Provider(...)`。
3. `WithBind`→`Bind`、`WithBeforeStart`→`BeforeStart` 等 15 组机械替换。
4. 机制相关（`Effect`/`Component`/`OnKeyChange`/`Reconcile`/`Provides`/`Requires`）加 `mech` 前缀。
5. `app.Database("sql")` 等 12 个访问器 → `app.Resource("database.sql")`。
6. 审计：不再依赖全局 `GetAuditor()`，改为把 `audit.Auditor` 显式传给 bundle/组件。
7. 契约：删除或白名单框架未实现的段（`script` 等）。
8. `WithConfigNamespace` 删除（env 名不变）。

### 6.4 需要同批发但**不在本设计范围内**的
codegen 的两个模板（`internal/codegen/app.go`、`internal/codegen/appspec_gen.go`）——
属另一设计者的「声明式装配 + 分层骨架」交付；本设计的接缝要求是：**模板只产出 1 次 `MustServe` 调用 + 域列表**（§1.3）。

---

## 7. 归因结论：装配复杂度的缺陷深度是否由 bald 框架模块导致

**结论：主要由框架结构导致（可修），示例放大了它（不可修也不必修），另有一组属有意取舍（不该改）。**

### 7.1 框架结构必须改（证据 → 判据）
| # | 框架缺陷 | 硬证据 | 命中判据 |
|---|---|---|---|
| F1 | **不存在「业务域」一等概念** | `AppSpec` 只有 `meta/server/components/capability/audit_backends/bundle_normalized`（`bconf/proto/bald/appspec/v1/appspec.proto:15-33`，无域字段）；生成物把业务入口留成 TODO 桩（`internal/codegen/appspec_gen.go:109` HTTP、`:214` gRPC `func registerGRPCService(s *grpc.Server) {}`） | **①（核心）** |
| F2 | **注册表抽象没被抽出** | 13 张表逐行同构；`appkit.Registry[T]`（`pkg/appkit/registry.go:18`）**全仓零引用**（含测试/示例/cmd） | ② |
| F3 | **每新增资源域需同步改 7 处** | S1 §3.3（registry 文件 / `bootstrapSpec` 字段 `pkg/appkit/bootstrap.go:104` / `With*Registry` / `build*` / 阶段 B 调用 / `Effect` 注册 / AppKit 字段+访问器 `pkg/appkit/appkit.go:77-97`） | ② |
| F4 | **手工段枚举表 + 已发生的静默漏消费** | `bootstrap/database.go:77-86`（注释 :69-71 自陈）、`bootstrap/server.go:100-141`（32 段仅 2 段 implemented）；**`script` 段零消费者**（§0.5） | ②（+ 隐性正确性） |
| F5 | **双入口 + 15 组双拼** | `New`(appkit.go:373) vs `FromBootstrap`(bootstrap.go:460)；15 组同义名（§0.4）；`AfterStart`/`AfterStop` 连双拼都不齐 | ④ |
| F6 | **唯一必填 Option 源于内部自指** | `WithConfigNamespace`(bootstrap.go:265) 缺则 :640-645 报错；自指论证见 `appkit.go:56-66` | ④ |
| F7 | **框架内部时序约束外泄为逃生舱** | `WithGRPCOptions`(:199)、`WithExtraServerFunc`(:239) 的注释自陈（:188-201、:229-241）；而根因已由「server 构造延后到 Run 期」根治（:533-543 + `wiring_order_test.go:27-65`）——**根因修了、舱留着** | ④ |
| F8 | **codegen 生成旧路径** | `internal/codegen/app.go:52`、`internal/codegen/appspec_gen.go:175` 都产 `appkit.New(...)` | ④（生成物即用户起点） |

### 7.2 示例（bald-admin/_example）导致 —— 框架无需改
| # | 现象 | 证据 | 为什么框架不用改 |
|---|---|---|---|
| E1 | 注释占装配非空行 40%（1,392/3,787） | S2 §2 | 脚手架自述文档，不是可执行装配 |
| E2 | 演示物冒充业务（`demo.heartbeat` 5s 心跳、`*/30` cron 探针、`bald:probe` handler）≈230 行 | S2 §6(ii) | 示例为演示「全接一遍」 |
| E3 | 21 域 + 9 个可选后端全接 | S2 §2.1 | 示例定位，不是框架要求 |
| E4 | 审计三后端 + 热切换双轨 350 行 | `app/audit.go` 204 + `app/audit_wiring.go` 146 | 演示 `audit.backends` 协调 |
| E5 | 业务种子数据 369 行写在装配根（占 `bootstrap.go` 40%） | S2 §6(ii) | 数据内容属应用 |

### 7.3 混合归因（框架缺钩子 + 示例选错落点）—— 需框架补钩子
- **业务能力策略住在装配层**（父母代理追加实证，已独立复核）：
  `cmd/bald-admin/app/builders.go:20-111` 构造 `buildLoginLimiter`/`buildLoginBreaker`/`buildLoginRetrier`，
  读的是**业务配置段** `login.rate_limit.*`/`login.breaker.*`/`login.retry.*`
  （`builders.go:38-48`、`:71-82` 注释即配置 schema），在 `assembly.go:508-522` 注入业务对象。
  即「登录限流阈值/熔断阈值/重试退避」这些**业务决策**住在装配目录，而 `internal/apiserver/biz/v1/auth/auth.go`
  只持有注入点（`SetLoginLimiter/SetLoginBreaker/SetLoginRetrier`，auth.go:195-213）。
- **框架缺的钩子**：没有「业务域自持自己的接线与配置」的入口（因 F1 无域概念）。
  补上 `appkit.Domain`（含 `Wire(app *App) error`）后，`builders.go` 的三个 builder 移进 auth 域包，
  装配层不再出现业务阈值 → 判据 ② 受益。
- **判定：框架要改（补域钩子），示例的落点是它「唯一能放的地方」。**

### 7.4 有意取舍 —— 不改
`stopAll` 五阶段 + `Stop` 传 `context.Background()`（appkit.go:621-666、doc.go:35）、
显式注册不用 `init()`（registrar.go:13-15、registry.go:6-8）、能力声明 fail-fast（capability.go:45-94）、
`Ais()` 返回副本（ai.go:72-77）、审计旁路不阻断（mount.go:176-193）。
**唯一被本设计推翻的既有取舍**是「两条装配入口并存」（S1 §6 判为取舍）：
理由是它的代价已经从「多一套 Option 类型」恶化成「15 组双拼 + 生成器产旧路径（F8）+
用户必须先自答『我在哪条路径上』」，收益（e2e 复用 `newApp`）可由 `NewApp` 单独保留 → **代价 > 收益，改为单入口**。

### 7.5 判据 3 的口径与现状（父母代理追加实证的判定）
- **业务 API（不算违规）**：`pkg/store`(19 文件)、`berrors`(11)、`pkg/contextx`(5)、`bconf/gen/go/bald/store/v1`(10)。
- **装配层（已达标）**：`pkg/appkit`、`bootstrap`、任何 `internal/**` —— 业务层 **零条**
  （守卫 `internal/apiserver/layering_invariant_test.go:34-69` 只锁这一条，且**用子串匹配而非解析 import** —— 守卫本身偏弱，建议改为 AST 检查）。
- **能力实现层（需判定）**：`cache/loadable`(2 文件)、`contrib/authn-jwt`(1)、`oss/minio`(2)、`oss/s3`(1)、
  `retry`/`ratelimit`/`circuitbreaker`/`pkg/audit`/`pkg/authn`/`log`（集中 `biz/v1/auth/auth.go:18-25`、`biz/v1/cachemonitor/cachemonitor.go:25`）。

**我的判定：能力包属「业务 API」，import 本身不违反判据 3；但"同一个能力被接两遍"所暴露的问题是真的，只是位置不在 import。**

独立复核（`grep` 全量，非照抄 S2）后的**修正版结论**：
- 父母代理追加的「同一能力被接两遍」**不是双份构造**。真实结构是：
  构造在装配层（`builders.go:20-111`），业务层只持有**注入点**（`auth.go:195-213` 三个 `Set*`）与**端口字段**
  （`auth.go:107` `ratelimit.Limiter`、`:112` `circuitbreaker.CircuitBreaker`、`:117` `*retry.Retrier`）。
  真正泄漏的是**端口类型的所有权**：业务要用框架能力包的接口/结构体做字段类型，于是必须 import。
- 类型形状决定泄漏是否可避免：`ratelimit.Limiter` 是**接口**（`ratelimit/ratelimit.go:25`）、
  `circuitbreaker.CircuitBreaker` 是**接口**（`circuitbreaker/circuitbreaker.go:56`）、
  但 `retry.Retrier` 是**具体结构体**（`retry/retry.go:46`）→ 第三个即使想用端口也做不到，除非框架补端口。

**回答「是否需要统一的接口注入门面」**
- **注入门面已经存在**（`Set*`，`auth.go:195-213`），**不需要再加一层**。加门面只会新增一个中间层。
- **要改的是落点，不是门面**：让**业务域自持**「读自己的配置段 → 构造自己的能力 → 持有自己的端口」，
  即 `appkit.Domain` 的 `Wire(app *App) error` 钩子（§7.3）。这样：
  - 装配层不再出现 `login.rate_limit.*` 这类业务阈值（判据 ② 受益）；
  - 业务层的 import 仍在（判据 3 不违规，因为 `ratelimit`/`retry`/`circuitbreaker` 是公开 API 包），
    但 import 的语义从「被迫去装配层取」变成「域自己声明的依赖」。
- **`retry.Retrier` 具体类型**建议框架补一个 `retry.Doer` 端口接口（1 个接口 + 1 处断言），
  否则业务永远绑死具体实现——这是**唯一一处**我建议为判据 3 加代码的地方。
- **影响判据**：②（主）、③（仅 `Retrier` 一处）、①（域钩子是判据 1 的前置）。

---

## 8. 四条判据的达标路径

| 判据 | 现状 | 达标路径 | 依赖 |
|---|---|---|---|
| ① 新增域只动 3 文件、零装配修改 | **不成立**：下界 7 文件/4 装配文件（permgroup 反例，S2 §4.1），全栈域 15+ 文件/6-7 装配（S2 §4.2） | 引入 `appkit.Domain` 一等概念（含 `Wire`/`Routes`/`Services`/`Stores`/`ConfigKeys`）+ 生成器产出 `domains_gen.go` 聚合 → 手写仅 proto/biz/handler | **另一设计者的生成器骨架（硬依赖）** |
| ② 装配行数不随域数增长 | **不成立**：14 处域相关装配引用/域（S2 §4.3 实测 282 处/21 域） | §3 段驱动单一 Provider 注册（消除 7 处同步与两张手工段表）+ §7.3 域自持 Wire → 每域装配改动 = 0 | 本设计批次 2 |
| ③ 业务不 import 装配层 | **实质成立**（零 `pkg/appkit`/`bootstrap`/`internal/**`）；口径需钉死 | 保持 + 加强守卫（`layering_invariant_test.go:34-69` 改 AST 匹配）+ 补 `retry.Doer` 端口 | 批次 1 |
| ④ 30 分钟跑起来 | **无计时证据**（S1 §5(iii).1） | §1.2 目标 main.go = 3 语句 / 0 必填 Option / 0 概念；需**实测计时**验证 | 批次 2+3 |

---

## 9. 未决问题
1. **`retry.Doer` 端口**是否值得加（1 接口 + 1 断言）？我倾向加，但它只服务 1 个业务文件（`auth.go:117`）。
2. **`Domain` 的契约表达**：是加进 `AppSpec`（proto 变更，需改 `appspec.proto:15-33`）还是新增 `domain.proto`？
   取决于另一设计者的生成器骨架（接缝）。
3. **`bootstrap/std` 是否该存在**：它把「显式注册」从 39 行压到 1 行，但引入了「bundle 包」这一新概念
   （代价：`std` 一旦被 import 就拉进 http/grpc/otel 全部依赖）。若判据 ④ 优先于「最小依赖图」，则选它。
4. **L3 段覆盖检查的缺省严格度**：默认报错（fail-closed）会拦住「契约先行」工作流；默认告警会重演 `script` 的静默。
   我选 fail-closed + 白名单，需作者确认。
5. **`AfterStop` 无双拼对应项**（`WithAfterStop` 不存在）——是遗漏还是有意？影响合并后的语义对齐。
6. **两遍配置合并（§3.4）的实现风险**：需确认 `bootstrap/config.Store` 支持「追加一层 env」而不重建，
   否则要改 `config` 包（新增 breaking）。**此项我未验证到底层，标为待验证。**

---

## 10. 与 S1/S2 的差异清单（本文件改动/新增的判断）
1. 导出符号 **147**（go doc 权威），非 S1 的 145。
2. 双拼 **15 组**，非 S1 的 14 组（漏 `AfterStart`/`WithAfterStart`）。
3. 默认日志分派实际在 **`pkg/appkit/bootstrap.go:1017-1088`**，任务书的 `bootstrap/bootstrap.go` 不存在。
4. **新证据**：`script` 契约段（`bootstrap.proto:64`）**零消费者** → 「静默无消费」已发生，非假设。
5. **新判断**：父母代理的「同一能力被接两遍」应修正为「端口类型所有权泄漏」；注入门面已存在，不必再加，
   要改的是**业务策略的落点**（`builders.go:20-111` → 域包）与 `retry.Retrier` 的具体类型绑定。
6. **推翻既有取舍**：「两条装配入口并存」从取舍改判为**应合并**（代价已被 F8 放大）。
