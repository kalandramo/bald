# Bald 日志设计：一个瘦契约、nop 默认、装配期注入后端的日志体系

Author(s): bald 团队
Last updated: 2026-09-13
Discussion at: `Bald 日志平面接口设计.md`（历史决策记录）、`设计评审-第三轮-2026-09-12.md`
Status: Accepted（已实现，随 bald v0.2.x 发布）

## 摘要

`bald/log` 为整个框架提供**唯一的日志抽象**：6 方法的 `Logger` 接口、并发安全的全局句柄、零成本的 nop 默认。契约层零第三方依赖；标准库 `log/slog` 适配器（`log/bslog` 子包，包名 `bslog`）开箱即用；五个远端/终端后端（loki / aliyun / tencent / sentry / charm）各自独立成 module，经 `contract` 子包接入契约装配。本文回答三个问题：为什么契约这么瘦、为什么后端独立成 module、为什么装配必须显式注册。最重要的承诺：**框架核心永远不 import 任何具体日志库，新后端接入对框架零改动。**

> 本文是按当前代码（v0.2.x）整理的设计文档，与《Bald 日志平面接口设计.md》（演进决策日志）互补：那篇记录"当时为什么这么改"，本文记录"现在是什么、为什么是这样"。

---

## 背景与动机

bald 融合的三个上游项目对日志的处理各不相同，其中一家的做法我们明确不想要。

**onexstack/pkg/log 是反面教材**：zap 胖封装成 14+ 方法接口，内嵌 kratos 与 gorm 日志接口，配置非法时 `NewLogger` 直接 panic。接口胖到只能由自己的实现满足，换后端等于换框架。onexstack 还同时存在三套日志抽象（`pkg/log`、`pkg/logger`、`pkg/otelslog`），业务不知道该用哪个。

**go-lulu/log 与 kratos/log 是正面的**：前者证明了"极简接口 + 全局注册 + 零后端默认"足以撑起一个框架；后者证明了薄封装标准库 `log/slog` 干净可行，其 `ContextWithAttrs` 上下文属性流值得保留。

bald 的需求一句话定性：**日志后端的选择是横切关注点，归进程入口（bootstrap）管，不归框架核心管。** 框架核心要打日志，但不能绑死任何日志库。

---

## 设计

### 三层架构与 module 布局

```text
log                    契约层    Logger 接口 + 全局句柄 + nop 默认 + ctx 属性流
                                 + MultiLogger + 包级便捷函数            【零三方依赖，独立 module】
log/bslog              适配器层  slog 后端 + Options/--log.* flags
                                 + FilterKey 脱敏 + lumberjack 轮转      【同 module 子包，包名 bslog】
log/{loki,aliyun,      后端层    直连各家 SDK 的实现                     【各自独立 module，
 tencent,sentry,charm}            根包零契约依赖                          根包 + contract/ 子包】
bootstrap              装配层    LogRegistry + BuildLogger               【另一个 module，消费 bconf】
                                 + SlogLoggerProvider + LogOptions
```

依赖方向单一向上：后端 → 契约；装配 → 契约 + 后端 + bconf；框架核心 → 仅契约。远端后端独立 module 是有意的——阿里云 SLS SDK 一个 import 就拖进 prometheus 全家桶（见 `log/aliyun/go.mod` 的 indirect 列表），module 边界让这份代价只由真正使用该后端的项目承担。

### 契约层：`Logger` 接口（log/log.go）

```go
type Level int // LevelDebug=0 .. LevelError=3，值越大越严重

type Logger interface {
	Debug(ctx context.Context, msg string, args ...any)
	Info(ctx context.Context, msg string, args ...any)
	Warn(ctx context.Context, msg string, args ...any)
	Error(ctx context.Context, msg string, args ...any)

	// Enabled 报告后端是否会输出给定级别，用于守卫昂贵参数构造。
	Enabled(level Level) bool

	// With 返回携带给定 key-value 的新 Logger，不可变语义。
	With(args ...any) Logger
}
```

```go
log.Info(ctx, "server started", "addr", ":8080")          // key-value 结构化输出

if log.Enabled(log.LevelDebug) {                          // 昂贵参数守卫
	log.Debug(ctx, "dump", "payload", expensiveRender())
}

moduleLog := log.With("module", "registry")               // 可长期持有的子 Logger
```

边界：无 Fatal/Panic（4 级对齐 slog，业务用 `Error` + `os.Exit`）、无 `f`/`w` 变体、不内嵌任何外部接口。

### 全局句柄与包级函数

- `SetLogger(l)`：注入后端，nil 回退 nop，RWMutex 保护并发安全；`GetLogger()`：取当前后端。
- 包级函数 `Debug/Info/Warn/Error/Enabled/With`（global_helpers.go）转发到当前全局后端。**框架内一律用包级函数，`log.GetLogger().Xxx` 链式写法已全量替换并禁止**（2026-09-13 惯例，192 处，bald `e1819d5` + bald-admin `de64f00`）。
- 语义差异注意：`log.With(...)` 每次基于**当前**全局后端派生，热更新后自然切到新后端；先 `GetLogger()` 捕获的旧实例不会切换。

### nop 默认与 ctx 属性流

- 未注入时全局句柄为 `nopLogger{}`：全部空实现、`Enabled` 恒 false。`import log` 零副作用，bald-crud 这类库可放心引用契约而不强迫宿主初始化日志。
- `ContextWithAttrs(ctx, attrs...)` / `ContextAttrs(ctx)`（context.go）：中间件在请求入口挂 `trace_id` 等字段，请求范围内日志自动携带。契约层只挂载/读取（私有 key，重复挂载合并），**不解释属性**——slog 适配器在落盘前合并进参数列表。observability 中间件经此路让每条请求日志带 `trace_id`；SpanContext 无效时 `middleware.LogTraceIDs` 生成随机兜底 ID，零配置也有可关联的日志链路。

### MultiLogger：多源广播（log/multi.go）

`NewMultiLogger(loggers ...Logger) Logger`——每条日志广播到全部子 Logger（本地 + 远程并存）。四级方法顺序广播、单个失败不影响其余；`Enabled` 任一子启用即启用（保守放行，多投的代价是带宽、漏投的代价是排障缺失证据）；`With` 对每个子 Logger 派生后重新组合；nil 构造期过滤，零参数等价 nop。它是纯 `Logger` 装饰器，故放契约层顶层。

### 默认后端：slog 适配器（log/bslog，包名 bslog）

包名 `bslog` 取 bald+slog 之意，目录=包名（2026-09-13 由 `slog`/`slogadapter` 改名——旧名目录≠包名导致全部消费点被 goimports 强制加别名），既避标准库冲突又免别名。pflag / lumberjack / errgroup 收敛在此子包，契约使用者二进制不受影响。

```go
type Options struct {
	Level       string         // debug|info|warn|error，默认 info
	Format      string         // console|json，默认 console
	OutputPaths []string       // "stdout"/"stderr"/文件路径，默认 ["stdout"]
	Rotate      *RotateOptions // lumberjack：MaxSize=100MB, MaxBackups=7, MaxAge=30d, Compress=true
}
// NewOptions / AddFlags(--log.level 等全套) / Validate
// New(o *Options, opts ...Option) log.Logger
```

三条行为边界，均为实测踩坑后确定：

1. **配置非法回退 info，不 panic**（与 onexstack 相反）——级别写错不值得崩进程；
2. **文件打开失败回退 stdout**——可观测性不因一个路径问题全丢；
3. **多目标 errgroup 并发写**，但为**复制分流**（每个目标收全量日志），不支持按级别分流到不同文件。

扩展点收在 `Option`：`WithFilter(FilterKey("password"))` 脱敏、`WithAttrs` 固定属性、`WithHandler`/`WithOTelHandler` 换底层 handler。脱敏的实现细节：slog 把 `WithAttrs` 固化的属性交给内层 handler 在 `Handle` 阶段直接合并，会绕过外层装饰器——`filterHandler.WithAttrs` 必须**先过滤再下沉**，否则 `logger.With("password", ...)` 的脱敏静默失效。

OTel 桥接刻意零依赖：核心不 import otel，`WithOTelHandler` 只是 `WithHandler` 的语义别名，调用方自带 `otelslog.NewHandler` 注入，otel 依赖树谁用谁背。

### 远端/终端后端 ×5 与 contract 子包模式

`log/{loki,aliyun,tencent,sentry,charm}` 各自独立 module，统一两包模式——根包直连 SDK 零契约依赖；`contract/` 子包是唯一 import bconf 处，导出 `Type` 常量 + `Provider(ctx, *bootstrapv1.Logger) (log.Logger, func(), error)`。

| 后端 | 定位 | 关键语义 |
| --- | --- | --- |
| `loki` | Grafana Loki 直推 | HTTP Push API；内存缓冲 batchSize=100 触发冲刷；`Close` 冲刷剩余缓冲；必填 endpoint |
| `aliyun` | 阿里云 SLS | 官方 producer 异步聚合；`Close(5000ms)` 关停 |
| `tencent` | 腾讯云 CLS | 官方 AsyncProducerClient；`Close(5000ms)` |
| `sentry` | 错误追踪 | `Error` 上报事件（带堆栈），其余级别记 breadcrumb；必填 DSN |
| `charm` | 本地开发终端 | Charmbracelet 彩色输出，默认 stderr + Info + 时间戳 |

诚实记录两个限制：**五后端目前都忽略 ctx**（`_ context.Context`），ctx 属性流只在 slog 适配器落地；**loki 无后台定时冲刷**，缓冲未满且不 `Close` 时日志停留在内存——停机 Effect 链对带缓冲的后端是必须兑现的。

### 装配层：注册表 + 三级工厂 + 两阶段

`bootstrap.LogRegistry` 按名注册后端工厂，**显式 `MustRegister`，无 `init()` 自注册**（blank import 是 no-op，bald 全框架反 init 纪律）：

```go
lr := bootstrap.NewLogRegistry()
lr.MustRegister(lokicontract.Type, lokicontract.Provider) // 用哪个注册哪个

logger, cleanup, err := lr.BuildLogger(ctx, cfg.GetLogger()) // 按契约 logger.type 查表
log.SetLogger(logger)
defer cleanup()
```

`BuildLogger` 全 fail-fast：cfg 为 nil、type 为空、type 未注册（错误列出全部已注册名）、provider 返回 nil Logger 均报错——日志是必需品，与配置源"nil=跳过"语义刻意不同。cleanup 恒非 nil，可直接 `defer`。

更常用的是 AppKit（`appkit.FromBootstrap`）三级工厂分发：

```text
WithLoggerFactory（显式工厂） > WithLogRegistry（契约查表） > 默认 slog 工厂
```

两阶段语义解决"契约装载过程的日志往哪打"：**阶段 A（契约装载前）回退默认 slog 保证启动日志可见；阶段 B 装载校验后按 `logger.type` 重建。** 契约热更新时 `rebuildLogger` 重建后端并原子替换 cleanup 钩子，失败只记错误不中断（换后端失败不应杀死正在服务的进程）。

已记录的形状差异：契约 `Slog.output_path` 为单值且无轮转段；`Options` 的多 `OutputPaths` + `Rotate` 目前仅 CLI/Options 直构路径可用，待契约补字段后装配层跟进。

---

## 理由与取舍

**为什么 6 方法瘦接口？** 接口越胖，用户自带后端的适配成本越高。onexstack 的 14+ 方法接口实际只有它自己的 zap 封装能实现，"可插拔"名存实亡。6 方法是打日志的最小完备集：四级是事实标准，`Enabled` 守卫昂贵参数，`With` 支持模块化标签——少了哪个都有场景卡住，多了哪个都开始绑定实现。

**为什么默认 slog 而非 zap？** 标准库零依赖、与 kratos 同源、`slog.Record` 可直接转 OTel（桥接天然）。zap 的性能优势对默认后端非刚需；真有极端吞吐需求的项目本来就会换后端，这正是接口存在的意义。**我们不选 zap，但保留你用 zap 的权利。**

**为什么 nop 默认而非 panic？** 库不该强迫宿主先初始化日志，更不该因此崩溃。忘记注入的最坏结果是没日志，不是进程死掉。这与"配置非法回退 info"是同一条原则：**日志系统的问题用日志系统的方式兜底，不用崩溃解决。**

**为什么远端后端独立 module 而 slog 是子包？** slog 的依赖轻且人人要用；云端 SDK 重且仅特定部署形态用。Go module 是依赖隔离的唯一硬边界（同 module 子包的依赖仍进 go.mod），用 module 边界把依赖代价精确到"用的人付"。

**为什么 contract 子包模式？** 后端根包保持纯 Logger 实现，不 import bconf 就能被直构/测试场景单独使用；把 proto 契约耦合压缩到几十行的 `contract` 包，后端实现与契约演进互不拖累。与全框架 registry 家族纪律一致。

**为什么显式注册而非 init() 自注册？** init 自注册让依赖关系不可见、重名冲突运行期随机爆发；显式注册把"支持哪些后端"变成 main 里可 grep 的一行行代码。go-wind 大量 init 自注册，bald 迁移时一律改造。

**被放弃的方案：**

| 方案 | 放弃原因 |
| --- | --- |
| onexstack 式胖接口 | 绑死实现，无法适配自有后端；三套抽象并存正是它的病 |
| 配置非法 panic | 启动期崩溃换不来正确性 |
| 搬运 gookit/slog 自研日志栈 | 与"slog 适配层"定位冲突，拖入 color/goutil/rotatefile 全家桶；轮转/脱敏/多输出用 lumberjack + Filter + multiWriter 等价实现 |
| OTel 依赖进核心 | 依赖树庞大，语义别名已足够表达意图 |
| zap 默认后端 | 性能非默认后端刚需 |

---

## 兼容性

纯增量、无破坏：契约层 API 自落地以来签名未变；包级函数是纯新增。唯一内部约定变更是"包级函数取代 `GetLogger().Xxx` 链式"——风格约定而非 API 破坏，已全量替换并纳入评审检查项。

跨 module 使用者注意（非破坏但易踩）：`bald/log` 是独立 module，外部引用须 require + replace 它本身；远端后端 contract 包同时依赖 `bconf` 与 `log` 两个 module，replace 要配齐；gopls 对嵌套 module 常报 BrokenImport 假阳性，以命令行 build/test 为准。

---

## 实现与过渡

全部已落地，无过渡安排：

- [x] 契约层：接口 / 全局句柄 / nop / ctx 属性流 / MultiLogger / 包级函数（含并发与广播语义单测）。
- [x] slog 适配器：Options + flags + Validate、console/json、lumberjack 按大小轮转、FilterKey 脱敏（含 WithAttrs 预过滤修复）、多目标并发写、OTel 别名。
- [x] 远端/终端后端 ×5（2026-09-06 移植 go-wind-plugins/log）：独立 module + contract 子包，契约各后端段全部有消费者。
- [x] 装配层：LogRegistry（显式注册、fail-fast、cleanup 恒非 nil）+ SlogLoggerProvider + LogOptions。
- [x] AppKit 集成：三级工厂、两阶段装载、热更新 `rebuildLogger` 原子换后端。
- [x] trace 关联闭环：observability 中间件经 `ContextWithAttrs` 挂 `trace_id`，零 TracerProvider 时随机 ID 兜底。
- [x] 框架内包级函数惯例全量替换（192 处）。

验证：`log` module 及各后端、`bootstrap` 均随 bald CI（build + vet + test -short）全绿。

关联文档：`Bald 配置系统设计.md`（四源配置）、`应用框架设计.md`（AppKit 生命周期）、`AppKit FromBootstrap 约定装配.md`（装配全景）、`指标抽象设计.md`（可观测性闭环）。
