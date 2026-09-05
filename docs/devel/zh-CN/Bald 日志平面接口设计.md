# Bald 日志平面接口设计

> 本文由《日志设计.md》全文并入（2026-09-05），是 bald 日志体系的**单一设计文档**。
> 适用包：`log`（契约层，独立 module `github.com/kalandramo/bald/log`）+ `log/slog`（适配器子包，包名 `slogadapter`）+ `bootstrap`（装配层）。
>
> 三层架构（2026-09-05 对齐 go-wind 三层拆分，与配置系统同构；布局对齐 transport——契约与实现同 module、包边界隔离依赖）：
>
> ```text
> log         契约层   Logger 接口 + SetLogger/GetLogger 全局表 + nop 默认 + ctx 属性流 + MultiLogger   【零依赖】
> log/slog    适配器层 slog 后端 + Options/CLI flags + FilterKey 脱敏 + lumberjack 轮转                【依赖 log + pflag/lumberjack/errgroup】
> bootstrap   装配层   LogRegistry + SlogLoggerProvider + BuildLogger（契约 → 后端）                    【依赖 log + log/slog + bconf】
> ```
>
> - 全部框架代码只 import 契约层；后端经进程入口显式注入：`bootstrap.BuildLogger` 产出实例 → `log.SetLogger`。
> - 新后端接入：注册新 `LoggerProvider`（如 zap，新建 `log/zap/` 子包），框架核心零改动；将来契约扩展多输出源（本地 + 远程）后，装配层循环产出并经 `log.NewMultiLogger` 合并——注册表形状已就绪。
> - 已知差异：契约 `Slog.output_path` 为单值、不含轮转段；Options 的 `OutputPaths []string` + `Rotate` 仅 CLI/Options 路径可用，待契约补字段后装配层跟进。
>
> 关联文档：`Bald 配置系统设计.md`（多源配置加载）、`应用框架设计.md`（AppKit 生命周期与注入）、`指标抽象设计.md`（可观测性闭环）
> 参考实现：`go-lulu/log`（极简契约 + 全局句柄）、`kratos/log`（slog 薄封装 + 装饰器）、`onexstack/pkg/otelslog`（slog→OTel 桥接）

`log` 包提供**框架级的日志契约与全局句柄**：核心仅定义接口、零后端实现，默认静默（nop），由用户在装配期注入具体后端（slog / zap / OTel / kratos …）。目标是让 bald 核心与子模块不依赖任何具体日志库，同时保持开箱即用与可观测性。

---

## 1. 日志平面接口

### 1.1 设计目标

1. **框架核心零日志依赖**：`log` 契约层只定义接口 + 全局注册 + nop 默认，不 import slog/zap/kratos。
2. **开箱即用**：提供基于标准库 `log/slog` 的后端，无需任何配置即可输出结构化日志（console/json）。
3. **可插拔后端**：在默认 slog 之外，用户可注入任意后端（zap / zerolog / kratos），框架与子模块只依赖 `Logger` 接口。
4. **可观测性一等公民**：通过可选 OTel handler 桥接 OpenTelemetry Logs，复用 onexstack `otelslog` 的干净转换思路。
5. **瘦接口**：不内嵌 kratos/gorm 接口，不提供 `f`/`w` 双形态，避免 onexstack `pkg/log` 的接口膨胀。
6. **多源配置**：日志配置（level/format/output）落入已有的四源配置模型（flag > env > 文件 > 远程）。

### 1.2 三个参考实现的取舍

| 框架 | 做法 | 借鉴点 | 规避点 |
| --- | --- | --- | --- |
| go-lulu/log | 6 方法瘦接口 + 全局 `Set/GetLogger` + nop 默认 + 零后端 | **契约与全局句柄模型**；`Enabled` 守卫；ctx 首参 | — |
| kratos/log | 直接封装 slog；`NewLogger` + `WithFilter` 装饰器；`ContextWithAttrs` | **slog 后端实现**；ctx 属性流；OTel contrib handler | 不要绑死其装饰器细节 |
| onexstack/pkg/log | zap 胖封装（14+ 方法 + `W(ctx)` + `AddCallerSkip` + 内嵌 kratos/gorm 接口） | `Options` 的 `--log.*` flag 形态 | **胖接口、绑死 zap、NewLogger 内 panic** |
| onexstack/pkg/otelslog | 纯 `slog.Handler`，`slog.Record`→OTel `log.Record` | **OTel 后端直接复用其 Handler** | 三套日志抽象分裂（不重蹈） |
| onexstack/pkg/logger | 另一套 8 方法统一接口 | — | 不与 go-lulu 式契约并存造成分裂 |

**核心决策**：bald 只保留**一个**日志契约（`log.Logger`），OTel 只是该契约的一个可选后端 handler，绝不引入第二套抽象。

### 1.3 核心抽象：`Logger` 接口

```go
// log/log.go
type Level int

const (
	LevelDebug Level = iota // 0
	LevelInfo               // 1
	LevelWarn               // 2
	LevelError              // 3
)

// Logger 是框架级最小日志契约。第一参数为 context.Context，便于后端
// 提取 traceID / request_id；无 context 时可传 nil。
type Logger interface {
	Debug(ctx context.Context, msg string, args ...any)
	Info(ctx context.Context, msg string, args ...any)
	Warn(ctx context.Context, msg string, args ...any)
	Error(ctx context.Context, msg string, args ...any)

	// Enabled 报告后端是否会输出给定级别，用于守卫昂贵参数构造。
	Enabled(level Level) bool

	// With 返回携带给定 key-value 的新 Logger（典型用法：标记模块/请求）。
	With(args ...any) Logger
}
```

**决策 ①：为什么第一参是 `context.Context`？**
与 go-lulu 一致——后端若支持上下文透传（trace 关联），可从中取 `trace_id`；调用方无 ctx 时传 `nil` 仍可用，不强制。

**决策 ②：为什么保留 `Enabled`？**
结构化日志常见痛点：为满足 `Debug(ctx, "detail", expensiveCompute())` 而每次都构造昂贵参数。提供 `Enabled(LevelDebug)` 守卫，与 go-lulu 对齐。

**决策 ③：为什么 `With` 返回新实例而非修改自身？**
保证并发安全与不可变性，子模块可放心 `moduleLog := log.GetLogger().With("module","registry")` 后长期使用。

### 1.4 全局句柄与默认行为

```go
// log/global.go
var (
	mu           sync.RWMutex
	globalLogger Logger = nopLogger{}
)

func SetLogger(l Logger)   // 注入后端；传 nil 回退 nop；并发安全
func GetLogger() Logger    // 子模块取共享实例，不依赖具体日志库
```

- 默认 `nopLogger{}`：零成本、丢弃一切，保证 `import log` 无副作用（仿 go-lulu）。
- `SetLogger` 带 `sync.RWMutex`，与日志调用并发安全；传 `nil` 回退静默默认。
- 子模块统一 `log.GetLogger()` 取实例，框架核心（appkit 启停/注册日志）亦如此。
- 多源广播：`NewMultiLogger(loggers ...Logger) Logger`（契约层通用装饰器，`log/multi.go`）——每条日志写全部后端（本地 + 远程并存），Enabled 任一即真，With 组合传播，nil sink 过滤。

---

## 2. 日志适配器

### 2.1 开箱即用后端：slog（`log/slog`，包名 `slogadapter`）

> 2026-09-05 布局调整：slog 适配器自独立 module 并入契约同 module 的子包 `log/slog`（包名 `slogadapter` 规避标准库 `log/slog` 冲突，同 `transport/http→httpserver`）。slog 生态重依赖（pflag/lumberjack/errgroup）收敛在子包，Go 按包链接不影响契约使用者的二进制。

```go
// log/slog（包名 slogadapter）
type Options struct {
	Level       string         // debug|info|warn|error（默认 info）
	Format      string         // console|json（默认 console）
	OutputPaths []string       // 默认 ["stdout"]
	Rotate      *RotateOptions // lumberjack 文件轮转（按大小切割）
}

func NewOptions() *Options
func (o *Options) AddFlags(fs *pflag.FlagSet)          // --log.* 含 --log.rotate.*
func NewSlogLogger(o *Options, opts ...Option) log.Logger // 包裹 slog.Handler，实现契约 Logger
```

- 底层复用标准库 `log/slog`，不引入第三方日志库。
- `Enabled` 基于 `slog.Handler.Enabled`；`With` 基于 `slog.With` 派生子 logger。
- 可选装饰器（仿 kratos `WithFilter`）：`WithFilter(FilterKey("password"))` 脱敏敏感字段。
- 上下文属性：`log.ContextWithAttrs(ctx, slog.String("request_id", id))`，ctx-aware 调用自动带属性（仿 kratos）。

**决策 ④：默认后端选 slog 而非 zap？**
标准库零额外依赖、与 kratos 同源、OTel 桥接天然（slog.Record 可直接转 OTel）。zap 等由用户经 adapter 注入，框架不绑定。

### 2.2 可观测性后端：OTel（可选）

```go
// log/slog：经 WithOTelHandler（= WithHandler 语义别名）注入，
// 无需把 otel 依赖钉进核心；调用方用 otelslog.NewHandler 装配。
slogadapter.WithOTelHandler(h slog.Handler) Option
```

- 将 `NewSlogLogger` 的 handler 替换为 `otelslog.NewHandler(name, ...)`，日志同时进入 OpenTelemetry Logs 管道。
- 不强制引入 OTel 依赖；仅在用户显式注入时启用（contrib 作为可选依赖）。
- 级别映射沿用 otelslog：`slog.LevelDebug→SeverityDebug`（依此类推），属性按 `slog.Kind` 转换。

### 2.3 桥接适配器（按需，不进入核心；当前未实现，预留）

为避免 onexstack 胖接口的内嵌做法，桥接以**独立文件**提供，核心 `Logger` 不内嵌任何外部接口。以下为预留规划（按需新建，不污染核心契约）：

| 预留文件 | 作用 |
| --- | --- |
| `log/zap/zap.go` | `NewZapAdapter(*zap.Logger) log.Logger`（用户想用 zap 时；对齐 `log/slog` 子包模式） |
| `log/kratos/kratos.go` | `ToKratos(log.Logger) kratoslog.Logger`（把 bald logger 喂给 kratos 组件） |
| `log/gorm/gorm.go` | `NewGormAdapter(log.Logger) gormlogger.Interface`（gorm 日志接入） |

### 2.4 能力边界与已否决选项（2026-09-01 评估落地）

> 针对 `gookit/slog`（自研独立日志库）的能力评估结论（完整评估曾存独立文档，现已并入本节作为决策记录）：
> **不搬运 gookit/slog 自研日志栈**——它与 bald「标准库 `log/slog` 适配层」的定位是替代 vs 适配关系，
> 且会拖入 `gookit/color`/`goutil`/`rotatefile` 全家桶。取其**能力意图**，用已有依赖实现等价能力：

| gookit/slog 能力 | 落地状态 | 方式 |
|---|---|---|
| 文件轮转（按大小 + 清理 + gzip） | ✅ 已落地（2026-09-01） | **lumberjack** 替换 `openWriter` 文件分支；`Options.Rotate`（proto 契约对齐，`--log.rotate.*`） |
| 文件轮转（按**时间**切割） | ⏸️ 未迁移 | lumberjack 不支持按时间切割；确需时引 `gookit/rotatefile` 或系统 logrotate |
| 彩色 / 模板化控制台 | ⏸️ 可选（阶段 2） | 引入 `tint`（仅标准库依赖）替换 console 的 `slog.NewTextHandler` |
| Processor（字段注入） | ✅ 已有等价 | `ContextWithAttrs` / `WithAttrs` |
| Filter / 脱敏 | ✅ 已有等价 | `FilterKey` |
| 多 Handler 同时输出 | ✅ 已有等价 | `openWriter` 的 `multiWriter`（**复制分流**） |
| Fatal/Panic 8 级 | ❌ 不迁移 | 标准库 slog 4 级；业务用 `Error` + `os.Exit` |
| 整套自研 Handler/Formatter/Record | ❌ 不引入 | 违背「标准库 slog 适配层」定位 |

**能力边界（诚实记录）**：
- lumberjack 仅**按大小**（`MaxSize`）触发切割，`MaxAge`/`MaxBackups` 只控制历史备份的保留时限与份数，**不是**按天/小时切割。
- 多目标输出是**复制分流**（每个目标收到全部日志），**不是**按级别分流；确需按级别落不同文件时，须构造多个不同 `Level` 的 handler 各挂独立 writer。
- 彩色控制台（tint）按需启用，不引入 `gookit/color` 全家桶。

---

## 3. 日志初始化

### 3.1 装配链（bootstrap，2026-09-05 起契约驱动）

装配层 `bootstrap` 提供 `LogRegistry` + `SlogLoggerProvider` + `BuildLogger`：读契约 `Logger` 段（bconf `bootstrapv1`）→ 构造后端 → 产出实例。进程入口在创建 AppKit 前完成初始化：

```go
// 进程入口（main）：契约驱动装配（推荐）。
logger, cleanup, err := bootstrap.BuildLogger(ctx, cfg.GetLogger())
if err != nil {
    panic(err)
}
log.SetLogger(logger)
defer cleanup()
```

不经契约的轻量注入（开发/测试/嵌入场景）：

```go
log.SetLogger(slogadapter.NewSlogLogger(slogadapter.NewOptions()))          // 开箱即用
log.SetLogger(slogadapter.NewSlogLogger(o,
    slogadapter.WithFilter(slogadapter.FilterKey("password")),
    slogadapter.WithOTelHandler(h)))                                        // 脱敏 + 接 OTel
log.SetLogger(myZapAdapter)                                                 // 自有后端
```

### 3.2 与 AppKit 的集成

> **设计原则（2026-08-24 拍板）**：日志是**横切关注点**，后端选择属于**进程入口（bootstrap）职责**，
> 不由 AppKit 编排层持有或注入。AppKit 只**消费**全局句柄 `log.GetLogger()`，不做全局副作用。

- 全局日志句柄由调用方（main）一次性 `log.SetLogger(...)` 设置，全进程共享。
- AppKit 启停、服务注册、错误路径等内部日志一律经 `log.GetLogger()` 输出，不持有、不注入、
  **不**在 `Run` 内改动全局状态（无 SetLogger/defer 还原副作用）。
- 配置加载：`LogOptions` 落入四源配置模型（flag > env > 文件 > 远程）。若日志级别需由配置驱动，
  可在 `main` 中 `BeforeStart` 前反序列化后 `log.SetLogger` 重建后端；AppKit 不参与该流程。

**为什么不让 AppKit 持有日志（避免职责偏离）？**
AppKit 定位是**应用编排层**，职责限于 Server 启停、注册、配置装配、钩子与可观察。
日志后端选择是跨层横切关注点（registry / server / config 都要打日志），若由 AppKit 注入并临时
改动全局句柄，会导致：① 编排层带全局副作用；② 与设计文档第 5 节 AppKit 字段边界分叉；
③ 多个 AppKit 实例互相干扰。因此剥离后 AppKit 回归纯编排，日志归 bootstrap 统一初始化。

### 3.3 设计取舍总结

| 决策 | 理由 |
| --- | --- |
| 单一瘦接口（6 方法） | 适配任何后端仅需实现 6 方法；拒绝 onexstack 的 14+ 胖接口 |
| 零后端 + nop 默认 | 框架核心 import 无日志副作用；用户可选注入 |
| 默认 slog 后端 | 标准库零依赖、OTel 桥接天然、与 kratos 同源 |
| OTel 仅是可选 handler | 可观测性一等公民但不强制依赖 |
| 上下文首参 | 支持 trace 透传；无 ctx 传 nil 仍可用 |
| 桥接独立成子包/文件 | 核心契约不被 kratos/gorm 接口污染 |
| 配置走四源模型 | 复用已有配置系统能力，日志配置即业务配置 |

### 3.4 实施记录（原 TODO，已全部完成）

- [x] 实现契约层 `log`：`log.go` / `global.go` / `nop.go` / `context.go` / `multi.go`（2026-09-05 收敛为契约+子包布局）。
- [x] 适配器子包 `log/slog`（包名 `slogadapter`）：Options/AddFlags/NewSlogLogger/Filter/Rotate/OTelHandler。
- [x] 迁移 AppKit Run 生命周期日志为结构化输出（经 `log.GetLogger()`）；**不**经 AppKit 注入/持有（方案 A：日志归进程入口 bootstrap 统一初始化，2026-08-24）。
- [x] 补充单测（nop 静默、Set/Get、级别、脱敏、ctx 属性、并发 6 项）。
- [x] OTel Logs 后端：经 `WithOTelHandler` 注入，无需把 otel 依赖钉进核心（2026-08-29）。
- [x] 可观测性闭环（`pkg/middleware/{gin,grpc}/observability.go`）：gin/grpc 中间件真正起 span，并把 `trace_id`/`span_id` 经 `log.ContextWithAttrs` 挂到请求 ctx——slog 后端消费 ctx 属性流，使请求范围内所有日志自动携带 trace_id。未配置全局 TracerProvider 时 no-op，零配置可跑（2026-08-29）。
- [x] 子模块日志契约统一：各子模块均经 `log.GetLogger()` 取共享实例（`moduleLog := log.GetLogger().With("module","xxx")`）。纯桥接子包（如 `pkg/registry`/`pkg/registry/kratos`）不打日志；凡需打日志的扩展点一律走 `log.GetLogger()`（2026-08-29 巡检确认）。
