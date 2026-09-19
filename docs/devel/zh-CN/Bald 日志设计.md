# Bald 日志设计：一个瘦契约、nop 默认、装配期注入后端的日志体系

> Author(s): bald 团队
>
> Last updated: 2026-09-13
>
> Discussion at: `设计评审-第三轮-2026-09-12.md`
>
> Status: Accepted（已实现，随 bald v0.5.0 发布）

## 摘要

`bald/log` 为整个框架提供**唯一的日志抽象**：6 方法的 `Logger` 接口、并发安全的全局句柄、零成本的 nop 默认。契约层根包源码零第三方依赖（bslog 子包的 pflag/lumberjack/errgroup 收敛在 module 内，不进契约使用方依赖树）；标准库 `log/slog` 适配器（`log/bslog` 子包，包名 `bslog`）开箱即用；五个远端/终端后端（loki / aliyun / tencent / sentry / charm）各自独立成 module，经 `contract` 子包接入契约装配；契约只有一个多值配置项 `logger.backends`——多项即多后端广播（本地 + 远程双写），单项即单后端。本文回答三个问题：为什么契约这么瘦、为什么后端独立成 module、为什么装配保持显式注册（反 init 纪律）而默认路径零依赖零代码。最重要的承诺：**框架核心永远不 import 任何具体日志库，新后端接入对框架零改动、不进默认依赖树。**

> 本文是按当前代码（v0.5.0）整理的设计文档。

---

## 背景与动机

bald 的需求一句话定性：**日志后端的选择是横切关注点，归进程入口（bootstrap）管，不归框架核心管。** 框架核心要打日志，但不能绑死任何日志库。

---

## 设计

### 三层架构与 module 布局

```mermaid
flowchart TB
    subgraph BOOT["bootstrap · 装配层【独立 module】"]
        REG["LogRegistry + BuildLogger<br/>+ BslogLoggerProvider + LogOptions"]
    end

    subgraph BACKEND["log/{loki, aliyun, tencent, sentry, charm} · 后端层【各自独立 module】"]
        BK["根包直连各家 SDK，零 bconf 依赖<br/>contract/ 子包：Type + Provider(ctx, *Logger_Backend)<br/>（经用户 MustRegister 接入装配）"]
    end

    subgraph ADAPT["log/bslog · 适配器层【log module 子包，包名 bslog】"]
        BS["slog 后端 + Options/--log.* flags<br/>+ FilterKey 脱敏 + lumberjack 轮转"]
    end

    subgraph CORE["log · 契约层【根包源码零三方依赖，独立 module】"]
        LC["6 方法 Logger 接口 + 全局句柄 + nop 默认<br/>+ ctx 属性流 + MultiLogger + 包级便捷函数"]
    end

    BCONF["bconf 契约（proto）"]

    BOOT -->|"装配层消费：契约 + bslog（后端接入由用户注册决定）"| LC
    BACKEND -->|"实现 Logger 契约"| LC
    ADAPT -->|"实现 Logger 契约"| LC
    BCONF -.->|"logger.backends 驱动查表装配"| BOOT
```

依赖方向单一向上：后端 → 契约；装配 → 契约 + bconf + bslog；框架核心 → 仅契约。远端后端独立 module 是有意的——阿里云 SLS SDK 一个 import 就拖进 prometheus 全家桶（见 `log/aliyun/go.mod` 的 indirect 列表），module 边界让这份代价只由真正使用该后端的项目承担；装配层只 import bslog（默认路径依赖增量为零），后端接入与否完全由用户注册决定。

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
- 包级函数 `Debug/Info/Warn/Error/Enabled/With`（global_helpers.go）转发到当前全局后端。**框架内一律用包级函数，`log.GetLogger().Xxx` 链式写法已全量替换并禁止**。
- 语义差异注意：`log.With(...)` 每次基于**当前**全局后端派生，热更新后自然切到新后端；先 `GetLogger()` 捕获的旧实例不会切换。

### nop 默认与 ctx 属性流

- 未注入时全局句柄为 `nopLogger{}`：全部空实现、`Enabled` 恒 false。`import log` 零副作用，bald-crud 这类库可放心引用契约而不强迫宿主初始化日志。nop 是内部实现不导出构造。
- `ContextWithAttrs(ctx, attrs...)` / `ContextAttrs(ctx)`（context.go）：中间件在请求入口挂 `trace_id` 等字段，请求范围内日志自动携带。契约层只挂载/读取（私有 key，重复挂载合并），**不解释属性**——slog 适配器在落盘前合并进参数列表。observability 中间件经此路让每条请求日志带 `trace_id`；SpanContext 无效时 `middleware.LogTraceIDs` 生成随机兜底 ID，零配置也有可关联的日志链路。

### MultiLogger：多源广播（log/multi.go）

`NewMultiLogger(loggers ...Logger) Logger`——每条日志广播到全部子 Logger（本地 + 远程并存）。四级方法顺序广播、单个失败不影响其余；`Enabled` 任一子启用即启用（保守放行，多投的代价是带宽、漏投的代价是排障缺失证据）；`With` 对每个子 Logger 派生后重新组合；nil 构造期过滤，零参数等价 nop。它是纯 `Logger` 装饰器，故放契约层顶层。

契约装配已接入：`logger.backends` 是契约里唯一的后端配置项——每项自带 `type` 与参数段，多项即广播（装配层逐项构造后经 MultiLogger 合并，任一项失败 fail-fast 并回滚已构造项），**单项直通不包广播层**（出口零多余包装）；`Enabled` 语义即 MultiLogger 的保守放行（任一后端启用该级别即写）。热更新/停机链路同样生效：重建时逐项重造并合并 cleanup，停机顺序释放全部后端。

### 默认后端：slog 适配器（log/bslog，包名 bslog）

包名 `bslog` 取 bald+slog 之意，目录=包名，既避标准库冲突又免别名。pflag / lumberjack / errgroup 收敛在此子包，契约使用者二进制不受影响。

```go
type Options struct {
	Level       string         // debug|info|warn|error，默认 info
	Format      string         // console|json，默认 console
	OutputPaths []string       // "stdout"/"stderr"/文件路径，默认 ["stdout"]
	Rotate      *RotateOptions // lumberjack：MaxSize=100MB, MaxBackups=7, MaxAge=30d, Compress=true
}
// NewOptions / AddFlags(--log.level 等全套) / Validate
// New(o *Options, opts ...Option) log.Logger            // 丢弃 cleanup（进程退出 OS 兜底）
// NewWithCleanup(o *Options, opts ...) (log.Logger, func()) // 关闭文件/轮转句柄（装配层用）
```

四条行为边界，均为实测踩坑后确定：

1. **配置非法回退 info，不 panic**——级别写错不值得崩进程；
2. **文件路径先自动创建缺失父目录，打开仍失败才回退 stdout**——直写与轮转两路径行为对称；可观测性不因一个路径问题全丢；
3. **多目标 errgroup 并发写**，但为**复制分流**（每个目标收全量日志），不支持按级别分流到不同文件；
4. **文件与 lumberjack 句柄经 `NewWithCleanup` 返回的 cleanup 释放**（`New` 丢弃它）——lumberjack 缓冲需 Close 触发落盘，不关则停机丢尾批、热更新重建后端泄漏句柄；`BslogLoggerProvider` 与 appkit 默认路径均走 `NewWithCleanup`（Windows 上 `t.TempDir` 对未关句柄删除失败，是该缺陷的暴露面）。

扩展点分两层。**bslog 的 `Option`**：`WithFilter(FilterKey("password"))` 精细脱敏（任意 `slog.Attr→slog.Attr` 变换函数）、`WithAttrs` 固定属性、`WithHandler`/`WithOTelHandler` 换底层 handler——均为 bslog 特有，其余五后端无对应（条目结构由各家 SDK 决定，无中间 handler 层可插）。**契约层 `NewFilterLogger(l, keys...)`**：全后端通用的脱敏装饰器，命中 key 的值统一掩码为 `***`（属性保留不丢弃），覆盖调用参数、`With` 派生属性、ctx 属性流三类来源；配置驱动（契约 `logger.filter_keys`，单选与 backends 全部后端统一生效，宁全勿漏——远端可检索平台恰是外泄风险最高处），留空零开销直通。两套并存按需选择：配置驱动全后端用 filter_keys，代码驱动仅 slog 的精细变换用 `WithFilter`。bslog 脱敏的实现细节：slog 把 `WithAttrs` 固化的属性交给内层 handler 在 `Handle` 阶段直接合并，会绕过外层装饰器——`filterHandler.WithAttrs` 必须**先过滤再下沉**，否则 `logger.With("password", ...)` 的脱敏静默失效。

OTel 桥接刻意零依赖：核心不 import otel，`WithOTelHandler` 只是 `WithHandler` 的语义别名，调用方自带 `otelslog.NewHandler` 注入，otel 依赖树谁用谁背。**OTel 不下沉到五后端**（有意裁定）：OTel Logs 与五后端是"后端选择"互斥关系而非叠加——要 OTel 管道用 bslog+`WithOTelHandler` 即可；选了 loki/SLS/sentry 已选定日志平台，双管道冗余；要经 collector 转发的场景由 collector 完成，后端无需感知。同理 **`WithHandler` 也不下沉**：它抽象的是 slog 的 handler 层，五后端条目构造即各家 SDK API 调用（SLS `LogContent`、CLS `Log_Content`、sentry Event、loki JSON、charm 原生），强行抽象等于重造一层适配器。

### 远端/终端后端 ×5 与 contract 子包模式

`log/{loki,aliyun,tencent,sentry,charm}` 各自独立 module，统一两包模式——根包直连 SDK 零 bconf 依赖（只实现 Logger 契约，不感知配置形状）；`contract/` 子包是唯一 import bconf 处，导出 `Type` 常量 + `Provider(ctx, *bootstrapv1.Logger_Backend) (log.Logger, func(), error)`（吃后端声明项，多后端合并由装配层负责，provider 恒见单后端视图）。

| 后端 | 定位 | 关键语义 |
| --- | --- | --- |
| `loki` | Grafana Loki 直推 | HTTP Push API；内存缓冲 batchSize=100 触发冲刷；`Close` 冲刷剩余缓冲；`With` 派生实例共享缓冲（任一 `Close` 全量冲刷）；必填 endpoint；`flush_interval` 是推送请求超时（非定时冲刷间隔） |
| `aliyun` | 阿里云 SLS | 官方 producer 异步聚合；`Close(5000ms)` 关停 |
| `tencent` | 腾讯云 CLS | 官方 AsyncProducerClient；`Close(5000ms)` |
| `sentry` | 错误追踪 | `Error` 上报事件（带堆栈），其余级别记 breadcrumb；必填 DSN |
| `charm` | 本地开发终端 | Charmbracelet 彩色输出，默认 stderr + Info + 时间戳 |

诚实记录一个限制：**loki 无后台定时冲刷**，缓冲未满且不 `Close` 时日志停留在内存——停机 Effect 链对带缓冲的后端是必须兑现的。

**ctx 属性流的跨后端能力（六后端语义一致）**：请求入口经 `ContextWithAttrs` 挂载的属性（如 `trace_id`），由全部六后端在**构造日志条目时同步合并**——bslog 在 handler 层合并，其余五后端经契约 helper `ContextAttrsToArgs(ctx)`（拍平为 kv 序列，无属性返回 nil）在入队/构建事件时提取；同步提取不受异步 flush 的 ctx 失效影响。合并顺序统一为 `With 属性 → ctx 属性 → 调用参数`，同名 key 后者覆盖前者（与 slog 语义一致）。设计边界：**取值用 ctx、IO 不绑 ctx**——日志发送不随请求 ctx 取消而丢弃（loki flush 自建 `Background+timeout`，producer 型后端由 SDK 管理重试）。

### 装配层：注册表 + 两级工厂 + 两阶段

`bootstrap.LogRegistry` 按名注册后端工厂，**显式 `MustRegister`，无 `init()` 自注册**（blank import 是 no-op，bald 全框架反 init 纪律）：

```go
lr := bootstrap.NewLogRegistry()
lr.MustRegister(lokicontract.Type, lokicontract.Provider) // 用哪个注册哪个

logger, cleanup, err := lr.BuildLogger(ctx, cfg.GetLogger()) // 按契约 backends 逐项查表
log.SetLogger(logger)
defer cleanup()
```

`BuildLogger` 全 fail-fast：cfg 为 nil、backends 为空、任一项 type 为空或未注册（错误列出全部已注册名并定位到项）、provider 返回 nil Logger 均报错——日志是必需品，与配置源"nil=跳过"语义刻意不同。单项 backends 直通（不包 MultiLogger），多项经 MultiLogger 广播合并；任一项失败回滚已构造项 cleanup。cleanup 恒非 nil，可直接 `defer`。

更常用的是 AppKit（`appkit.FromBootstrap`）两级工厂分发：

```text
WithLogRegistry（契约查表，注册名单自控） > 默认纯函数路径（零 Option 零依赖）
```

注册表路径机制全权（查表 / backends 合并 / 出口脱敏 / fail-fast）；默认路径无注册表——阶段 A 回退 bslog、slog 项直构带 deco、非 slog 项 fail-fast 教学报错（错误信息直接给出 `WithLogRegistry` 三行用法）。两条路径的使用场景、nil 双语义与装饰器（deco）分级的设计论证，独立成篇见 **`AppKit 日志装配设计.md`**——AppKit 只持有装配策略（生命周期、用户 Options、兜底语义），查表构造/backends 合并/出口脱敏等机制全部委托本文所述 `LogRegistry`。

默认路径依赖增量严格为零：bslog 本就是 bootstrap 的直接依赖，不注册就不 import 后端包，SDK 不进依赖树。远端/自定义后端三行注册即契约可达：

```go
reg := bootstrap.NewLogRegistry()
reg.MustRegister(lokicontract.Type, lokicontract.Provider) // log/loki/contract
appkit.FromBootstrap(cfg, appkit.WithLogRegistry(reg))
```

默认路径三分派：阶段 A（契约装载前）回退默认 bslog；阶段 B 逐项分派 backends——slog 项走 `LogOptions` + 业务装饰器直构（`WithLogDecorators` 是 `bslog.Option`，仅 bslog 后端可消费），非 slog 项 fail-fast 教学报错；backends 空清单 fail-fast。**绝不静默降级。**此形态同时修复历史缺陷：默认路径曾**静默忽略 `logger.type`**（配 `type: loki` 不报错却产出 bslog），现在任何配置错误都 fail-fast 且可行动。

两阶段语义解决"契约装载过程的日志往哪打"：**阶段 A（契约装载前）回退默认 slog 保证启动日志可见；阶段 B 装载校验后按 `logger.backends` 重建。** 契约热更新时 `rebuildLogger` 重建后端并原子替换 cleanup 钩子，失败只记错误不中断（换后端失败不应杀死正在服务的进程）。换后端时旧钩子被同步兑现（先切全局句柄再冲刷旧后端）——带缓冲后端（loki 尾批）不因热切换丢日志；停机 Effect 链同样释放最新钩子。

### 业务接入示例：零代码装配

操作手册（启用各后端的三步、五后端字段速查、报错解读）独立成篇见 **`docs/guide/zh-CN/Bald 日志使用.md`**，本节保留设计视角的示例。

业务 `main.go` 不写任何日志代码——默认路径开箱即用（slog），默认配置即 `backends` 单 slog 项：

```go
// main.go 全部日志相关代码：没有。
app := appkit.FromBootstrap(cfg) // Bind/装载/校验/两阶段日志/热更新全内化
```

本地多输出 + 轮转（`configs/app.yaml`，flag > env > 本地文件 > 远程四源同构）：

```yaml
logger:
  backends:
    - type: slog
      slog:
        level: info
        format: json
        output_paths: ["stdout", "/var/log/myapp/app.log"] # 控制台 + 文件双写（复制分流）
        rotate:          # 仅对文件目标生效（lumberjack）
          enabled: true
          max_size: 100  # MB；只写关心的字段，缺省回退默认（100/7/30/gzip）
          max_backups: 7
          max_age: 30    # 天
          compress: true
```

切远端 Loki：`backends` 换 loki 项 + 三行注册（注册写法与 yaml 示例见 `_example/bald/configs/bald-demo.yaml` 内注释及《Bald 日志使用》）：

```go
reg := baldbootstrap.NewLogRegistry()
reg.MustRegister(lokicontract.Type, lokicontract.Provider)
app := appkit.FromBootstrap(cfg, appkit.WithLogRegistry(reg))
```

```yaml
logger:
  backends:
    - type: loki
      loki:
        endpoint: http://loki:3100/loki/api/v1/push
        labels: { app: myapp }
```

本地 + 远程双写即列表加项（逐项独立 level/format；**含远端项需 `WithLogRegistry` 注册路径**——默认路径仅接受 slog 项）：

```yaml
logger:
  filter_keys: ["password", "access_token"]   # 全局脱敏：全部后端统一掩码 ***
  backends:
    - type: slog          # 本地：stdout 全量排障
      slog: { level: debug, format: console, output_path: stdout }
    - type: loki          # 远程：生产级采集（level 独立过滤）
      loki: { endpoint: http://loki:3100/loki/api/v1/push, labels: { app: myapp } }
```

`filter_keys` 是 Logger 顶层全局字段（与 `backends` 清单正交）：对全部后端生效，装配层在最终 Logger（单项直通或合并后的 MultiLogger）出口统一包装 `log.NewFilterLogger`——覆盖调用参数、`With` 派生、ctx 属性流三类来源，全部后端共享同一份过滤。

热更新：`WithWatchConfig(true)` 下改 yaml 即重建后端（副本试装载 + 校验 + 原子替换），改坏只记错不杀进程。仍需代码的场景：业务装饰器（脱敏/固定属性，`WithLogDecorators`）与远端/自定义后端注册（`WithLogRegistry`）。

契约形状差异已收口：`Slog` 段补齐 `output_paths`（多输出，优先于单值 `output_path`）与 `rotate` 段（enabled / max_size / max_backups / max_age / compress，零值字段回退 bslog 默认 100MB/7 份/30 天/gzip），`LogOptions` 全量映射——契约装配与 CLI/Options 直构路径能力对齐，多目标复制分流 + lumberjack 轮转纯配置声明即生效。单值 `output_path` 保留（`output_paths` 的便捷简写，回退序固定），既有配置零迁移。

---

## 理由与取舍

**为什么 6 方法瘦接口？** 接口越胖，用户自带后端的适配成本越高。onexstack 的 14+ 方法接口实际只有它自己的 zap 封装能实现，"可插拔"名存实亡。6 方法是打日志的最小完备集：四级是事实标准，`Enabled` 守卫昂贵参数，`With` 支持模块化标签——少了哪个都有场景卡住，多了哪个都开始绑定实现。

**为什么默认 slog 而非 zap？** 标准库零依赖、与 kratos 同源、`slog.Record` 可直接转 OTel（桥接天然）。zap 的性能优势对默认后端非刚需；真有极端吞吐需求的项目本来就会换后端，这正是接口存在的意义。**我们不选 zap，但保留你用 zap 的权利。**

**为什么 nop 默认而非 panic？** 库不该强迫宿主先初始化日志，更不该因此崩溃。忘记注入的最坏结果是没日志，不是进程死掉。这与"配置非法回退 info"是同一条原则：**日志系统的问题用日志系统的方式兜底，不用崩溃解决。**

**为什么远端后端独立 module 而 slog 是子包？** slog 的依赖轻且人人要用；云端 SDK 重且仅特定部署形态用。Go module 是依赖隔离的唯一硬边界（同 module 子包的依赖仍进 go.mod），用 module 边界把依赖代价精确到"用的人付"。显式注册形态下此边界最彻底：只 import 契约层或 slog 的库零新增依赖，装配层只 import bslog——后端 module 完全不进 bootstrap 依赖树，注册权即依赖权，与 bconfig 捆绑全部配置源 SDK 形成对照（介意者经 `WithLogRegistry` 精选）。

**为什么 contract 子包模式？** 后端根包保持纯 Logger 实现，不 import bconf 就能被直构/测试场景单独使用；把 proto 契约耦合压缩到几十行的 `contract` 包，后端实现与契约演进互不拖累。与全框架 registry 家族纪律一致。

**为什么显式注册而非 init() 自注册？** init 自注册让依赖关系不可见、重名冲突运行期随机爆发；显式注册把"支持哪些后端"变成可 grep 的代码——这张名单就写在用户 main 里（注册了什么，import 树里就有什么）。go-wind 大量 init 自注册，bald 迁移时一律改造。

**被放弃的方案：**

| 方案 | 放弃原因 |
| --- | --- |
| 配置非法 panic | 启动期崩溃换不来正确性 |
| 搬运 gookit/slog 自研日志栈 | 与"slog 适配层"定位冲突，拖入 color/goutil/rotatefile 全家桶；轮转/脱敏/多输出用 lumberjack + Filter + multiWriter 等价实现 |
| OTel 依赖进核心 | 依赖树庞大，语义别名已足够表达意图 |
| zap 默认后端 | 性能非默认后端刚需 |
| 单 type + backends 双写法并存 | 同填静默忽略违反反静默哲学；两套形状让校验/文档/教学永远多一个分支 |

---

## 兼容性

契约层 API（接口/全局句柄/包级函数/MultiLogger/`NewFilterLogger`）自落地以来签名未变；backends、filter_keys、output_paths/rotate 均为纯增量字段，既有配置零迁移。破坏性变更一处：**v0.5.0 收缩删除 `type: nop` 契约类型**（含 `log.NewNop()` 导出、`bootstrap.NopLoggerProvider`、proto `NOP=15`）——消费者已查证零引用，配置残留会在启动期 fail-fast；破坏面与迁移路径详见《AppKit 日志装配设计.md》兼容性章节。内部风格约定"包级函数取代 `GetLogger().Xxx` 链式"已全量替换并纳入评审检查项。

跨 module 使用者注意（非破坏但易踩）：`bald/log` 是独立 module，外部引用须 require + replace 它本身；远端后端 contract 包同时依赖 `bconf` 与 `log` 两个 module，replace 要配齐；gopls 对嵌套 module 常报 BrokenImport 假阳性，以命令行 build/test 为准。

---

## 实现与过渡

全部已落地，无过渡安排：

- [x] 契约层：接口 / 全局句柄 / nop / ctx 属性流 / MultiLogger / 包级函数（含并发与广播语义单测）。
- [x] slog 适配器：Options + flags + Validate、console/json、lumberjack 按大小轮转、FilterKey 脱敏（含 WithAttrs 预过滤修复）、多目标并发写、OTel 别名。
- [x] 远端/终端后端 ×5：独立 module + contract 子包，契约各后端段全部有消费者。
- [x] ctx 属性流六后端全量落地：契约 `ContextAttrsToArgs` helper + aliyun/tencent/loki/sentry/charm 入队时同步合并，含 key 覆盖顺序与 nil ctx 单测。
- [x] loki With 派生实例缓冲共享修复：`shared` 状态结构体重构，派生实例写入同一缓冲、任一实例 `Close` 全量冲刷（httptest 端到端回归测试）。
- [x] 装配层：LogRegistry（显式注册、fail-fast、cleanup 恒非 nil）+ BslogLoggerProvider + LogOptions。内置全量注册表（`NewBuiltinLogRegistry` / `RegisterBuiltinLogProviders`）撤销——依赖膨胀不成比例，五后端 SDK 依赖从 bootstrap 退场。
- [x] 契约 nop 类型：零业务消费，`NewNop()` 导出与 proto `NOP = 15` 一并删除；`nopLogger{}` 内部实现保留为全局默认。
- [x] AppKit 集成：两级工厂（`WithLogRegistry` 机制全权 > 默认纯函数四分派 + 教学报错，修复 `logger.type` 静默忽略缺陷；`WithLoggerFactory` 全接管工厂已删除——与 LoggerProvider 签名重复）、两阶段装载、热更新 `rebuildLogger` 原子换后端。
- [x] Slog 契约补齐多输出 + 轮转：proto `output_paths` + `Rotate` 段，`LogOptions` 全量映射（output_paths 优先 / 零值回退默认），bconf 校验（空串项 / 负值 fail-fast）——契约装配与 CLI/Options 直构路径能力对齐。
- [x] 多后端广播契约接入：proto `Logger.backends` + `BuildLogger` 逐项构造（MultiLogger 合并、单项直通、失败回滚）——MultiLogger 从纯装饰器升级为契约可达能力，本地 + 远程双写纯配置声明。
- [x] 契约唯一化瘦身：删单 `type` 顶层选法与八个未实现后端段，provider 签名改吃 `*Logger_Backend`、`LoggerView` 视图层删除（provider 恒见单后端项，无需归一化）、bconf 校验/默认值与全部测试随迁——契约只为已实现的后端承诺形状。
- [x] bslog 直写文件路径自动创建父目录：对齐 lumberjack 轮转路径的首写 MkdirAll——修复嵌套目录缺失时静默回退 stdout 的不对称。
- [x] 全后端脱敏 `NewFilterLogger` + `logger.filter_keys` 契约：契约层通用装饰器（调用参数/With 派生/ctx 属性流三级来源全覆盖、空清单零开销直通）+ proto `filter_keys=2`（契约唯一化瘦身后 `Logger` 消息字段重排，bconf 空串项 fail-fast）+ `BuildLogger`/appkit 出口统一包装（单选、backends、type=slog 特例三路径一致）——远端可检索平台（loki/SLS/CLS/sentry）纯配置声明即脱敏；WithHandler/OTel 明确不下沉五后端（互斥后端选择 + 无统一 handler 层可抽象）。
- [x] trace 关联闭环：observability 中间件经 `ContextWithAttrs` 挂 `trace_id`，零 TracerProvider 时随机 ID 兜底。

验证：`log` module 及各后端、`bootstrap` 均随 bald CI（build + vet + test -short）全绿；backends 多后端广播另经 `_example/bald` e2e 冒烟（双后端独立 level/format、坏值 fail-fast 带 `backends[i]` 定位、文件路径父目录自动创建）。

---

## 附录：演进决策记录

### AppKit 为什么不持有日志

日志是横切关注点，后端选择属于进程入口（bootstrap）职责，AppKit 只消费全局句柄、不做全局副作用。若由 AppKit 注入并临时改动全局句柄：① 编排层带全局副作用；② 与 AppKit 字段边界分叉；③ 多个 AppKit 实例互相干扰。（FromBootstrap 装配路径是"进程入口委托"形态——main 把装配权交给 FromBootstrap，副作用归属入口而非编排层，边界仍成立。）

### gookit/slog 能力评估

它与 bald「标准库 `log/slog` 适配层」的定位是替代 vs 适配关系，且拖入 `gookit/color`/`goutil`/`rotatefile` 全家桶。取其**能力意图**，用已有依赖等价实现：

| gookit/slog 能力 | 落地状态 | 方式 |
|---|---|---|
| 文件轮转（按大小 + 清理 + gzip） | 已落地 | **lumberjack** + `Options.Rotate` |
| 文件轮转（按**时间**切割） | 未迁移 | lumberjack 不支持；确需时引 `gookit/rotatefile` 或系统 logrotate |
| 彩色 / 模板化控制台 | 可选（阶段 2） | 引入 `tint`（仅标准库依赖）；charm 后端已覆盖彩色终端场景 |
| Processor（字段注入） | 已有等价 | `ContextWithAttrs` / `WithAttrs` |
| Filter / 脱敏 | 已有等价 | `FilterKey` |
| 多 Handler 同时输出 | 已有等价 | `multiWriter`（复制分流） |
| Fatal/Panic 8 级 | 不迁移 | slog 4 级；业务用 `Error` + `os.Exit` |
| 整套自研 Handler/Formatter/Record | 不引入 | 违背「标准库 slog 适配层」定位 |

能力边界（诚实记录）：lumberjack 仅按 `MaxSize` 触发切割，`MaxAge`/`MaxBackups` 只控制历史备份保留，**不是**按天/小时切割；多目标输出是**复制分流**，不是按级别分流——确需按级别落不同文件时，用 `logger.backends` 声明多个不同 level 的后端。

### 桥接适配器预留（未实现，按需新建子包，不污染核心契约）

| 预留位置 | 作用 |
|---|---|
| `log/zap/zap.go` | `NewZapAdapter(*zap.Logger) log.Logger`（对齐后端独立 module 模式） |
| `log/kratos/kratos.go` | `ToKratos(log.Logger) kratoslog.Logger`（把 bald logger 喂给 kratos 组件） |
| `log/gorm/gorm.go` | `NewGormAdapter(log.Logger) gormlogger.Interface`（gorm 日志接入） |

### 关联文档

`AppKit 日志装配设计.md`（AppKit 侧两级工厂与 deco 分级——本文装配策略的展开篇）、`Bald 配置源层设计.md`（配置源层：能力轴与 provider）、`应用框架设计.md`（AppKit 生命周期）、`AppKit FromBootstrap 约定装配.md`（装配全景）、`Bald 指标设计.md`（可观测性闭环）。
