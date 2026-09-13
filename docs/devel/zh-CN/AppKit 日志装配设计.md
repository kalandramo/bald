# AppKit 日志装配设计：两条路径——注册表全权与零代码默认，`logger.type` 永不静默失效

Author(s): bald 团队
Last updated: 2026-09-13
Discussion at: `Bald 日志设计.md`（日志体系全景）· `AppKit FromBootstrap 约定装配.md`（装配速查）
Status: Accepted（v0.4.0 形态；v0.3.0 的三级工厂与内置全量注册表为同日收缩前形态，见"演进记录"）

## 摘要

AppKit 不持有任何日志实现，只持有**装配策略**。日志工厂按两条互斥路径分发：显式 `WithLogRegistry`（契约查表，注册名单用户自控，bootstrap 机制层全权）与**默认路径**（零 Option 零配置，固定行为——nil 回退 bslog、`type: slog` 直构带 deco、其余 fail-fast 教学报错）。装配机制（查表构造、backends 合并、出口脱敏）全部委托 `bootstrap.LogRegistry`，AppKit 只留机制表达不了的策略。最重要的承诺：**默认路径零代码零配置即有可见日志，`logger.type` 配置错误 fail-fast 且错误信息直接给出修复用法，绝不静默降级；默认路径的依赖增量严格为零。**

> 本文是按当前代码（v0.4.0，`pkg/appkit/bootstrap.go`）整理的设计文档。文末"演进记录"诚实收录了同一天内"三级工厂 + 七后端内置全量 → 两级 + 删除"的完整绕圈与教训。

---

## 背景与动机

### 痛点：默认路径曾静默吞掉 `logger.type`

2026-09-13 之前的默认路径固定产出 bslog 工厂——业务在契约里写 `logger.type: loki`，不报错、不生效，产出的是 slog。这是最坏的失败模式：不是崩溃，是沉默。排障时发现日志进了错误的平台，配置却看起来完全正确。

### 修复路上的两个目标曾被打包决策

为修这个缺陷，同日上午落地了"内置全量注册表"（七后端预注册，零代码配置即用），并顺手补齐了 nop 契约类型。当日下午复盘发现这笔决策捆绑了两个目标，代价不成比例（详见"演进记录"）。本文描述的是收敛后的最终形态。

### 机制与策略的边界

`bootstrap.LogRegistry.BuildLogger` 是通用机制：查表、构造、backends 合并、出口脱敏。但装配日志还有一批**策略问题**机制层回答不了：契约装载之前（阶段 A）日志往哪打、装饰器（deco）怎么交给"可能选中、可能选不中"的后端、默认路径不注册表时如何报错才有教学价值。这些属于生命周期与用户面的策略，塞进通用注册表会把机制打出洞。本文记录这条边界是怎么划的。

---

## 设计

### 分层总览：AppKit 是策略壳，bootstrap 是机制核

```text
appkit（策略层：生命周期、用户 Options、兜底语义）
  resolveLoggerFactory 两级分发
  ├─ WithLogRegistry（用户自选注册表）──> bootstrap.LogRegistry.BuildLogger
  │                                       （机制层全权：查表构造、backends
  │                                         合并、出口脱敏、未注册 fail-fast）
  └─ 默认（零 Option，纯函数分派，无注册表）
       ├─ 阶段 A（l==nil）→ bslog 回退（+deco）
       ├─ type=slog → LogOptions + deco 直构
       ├─ backends 非空 → slog 子项直构，非 slog 子项 fail-fast 教学
       └─ 其余 type → fail-fast 教学
```

两条路径**代码零共享**、语义彻底解耦：注册表路径是完整机制（用户名单、查表、合并、脱敏全自动）；默认路径是固定行为（能做什么写死，做不到的直接教你怎么切到注册表路径）。

### WithLogRegistry：契约查表、注册名单自控

```go
reg := baldbootstrap.NewLogRegistry()
reg.MustRegister(lokicontract.Type, lokicontract.Provider)     // 精选：用哪个注册哪个
reg.MustRegister("mylog", myProvider)                           // 或追加自定义后端
appkit.FromBootstrap(cfg, appkit.WithLogRegistry(reg))
```

**适用场景**——一切默认路径表达不了的需求：

- **远端/自定义后端**：loki / aliyun / tencent / sentry / charm 五后端与自有后端，注册即契约可达（`logger.type` / `logger.backends` 全语义）；
- **二进制体积控制**：不注册就不 import，对应 SDK 不进依赖树——注册表是依赖控制与可扩展的同时解；
- **后端行为定制**：想注册 deco-aware 的 slog provider（消费 `WithLogDecorators`）也走这里。

**边界**：deco（`bslog.Option`）在此路径的阶段 B 不直接生效——用户有 `MustRegister` 注入点，想要的语义一行注册就能表达（框架不重复代劳，避免双真相源）。阶段 A 回退仍吃 deco。

**机制层契约**（`bootstrap.LogRegistry`，详见《Bald 日志设计》）：显式 `MustRegister` 无 init 自注册、type 未注册 fail-fast 且错误列出全部已注册名、cleanup 恒非 nil、`BuildLogger` 出口统一脱敏包装。

### 默认路径：零代码、固定行为、教学报错

不声明任何 Option 时的路径。**无注册表**——四分派是纯函数，能做的事编译期写死：

| 分派 | 行为 | 理由 |
| --- | --- | --- |
| 阶段 A（`l==nil`） | 回退默认 bslog（+deco） | 契约装载前保证启动日志可见 |
| `type=slog` | `LogOptions` 映射 + deco 直构 `bslog.New` | deco 是 `bslog.Option`，仅 bslog 后端可消费 |
| `backends` 非空 | slog 子项直构合并；非 slog 子项 fail-fast | 与单选对称，多后端广播留给注册表路径 |
| 其余 type | fail-fast 教学报错 | 诚实报错优于静默降级 |

教学报错直接给出修复用法，不是冷冰冰的"不支持"：

```text
logger.type "loki": 默认路径仅支持 "slog"；远端/自定义后端需显式注册：
    reg := bootstrap.NewLogRegistry()
    reg.MustRegister(lokicontract.Type, lokicontract.Provider)
    appkit.FromBootstrap(cfg, appkit.WithLogRegistry(reg))
```

**为什么默认不查表**：查表需要注册表，注册表需要 import 后端包——`type: loki` 零代码即用意味着 loki SDK（及 aliyun/tencent/sentry/charm 全家）进每个默认路径用户的依赖树。修复"静默失效"只需要 fail-fast，不需要全量预注册；默认路径的依赖增量因此严格为零（bslog 本就是 bootstrap 的直接依赖）。

### 两阶段生命周期：nil 的两种语义

同一个 `nil`，两层各自正确地做出相反反应：

- **bootstrap 机制层**：`BuildLogger(ctx, nil)` fail-fast——直用注册表的用户给了空配置就是编程错误；
- **AppKit 策略层**：工厂收到 `l=nil` 意味着**契约还没装载**（阶段 A），回退默认 bslog——契约装载过程（用户 ConfigProvider 连远端、认证、重试）的日志必须可见，否则 consul 重试 30 秒期间终端一个字不出。

阶段 B（装载+校验后）按契约重建。**热更新换后端时旧钩子同步兑现**：先切全局句柄（新日志进新后端）再冲刷旧后端（`rebuildLogger`）——带缓冲后端（loki 尾批）不因热切换丢日志。启动链任何一步失败，全局句柄恢复装配前的值——写全局被认真当作可逆效应对待。

### 脱敏单层保证：出口包装 + 视图递归

`filter_keys` 要求全部后端恰好包一层 FilterLogger，全部路径语义一致，靠结构性机制而不是约定：

1. **bootstrap 出口包装**：`BuildLogger` 出口统一包 `NewFilterLogger`；backends 子项递归时传 `LoggerView(b)` 视图——proto `Backend` 消息没有 `filter_keys` 字段，视图恒空清单，空参 `NewFilterLogger` 恒等直通。**天然只在顶层包一次，写错都错不出来。**
2. **AppKit 补直构路径**：默认路径的 backends 合并与 `type=slog` 直构不经过 `BuildLogger`，AppKit 在自己的出口包；`WithLogRegistry` 路径整体委托，出口已包，不重复。

双层包装不是正确性 bug（掩码幂等）但是热路径浪费，更是失控信号——包装责任点越少越不可能漏或重。

---

## 理由与取舍

### 为什么恰好两条路径，不是一条或三条？

两条路径对应两类真实需求，缺一条都有人被迫写不该写的代码：

- 只有注册表：零代码用户被迫写三行无业务信息的注册样板；
- 只有默认：用远端后端的用户没有入口，自定义后端无处安放。

而 v0.3.0 的第三条（`WithLoggerFactory` 全接管工厂）被删除——它的函数签名与 `LoggerProvider` 完全相同，本质是"不挂名字的 provider"：任何工厂逻辑包一层 `MustRegister` + 契约 `type: xxx` 即等价表达。它删除时一并清掉三个设计债：nil 隐式契约（工厂作者必须处理 nil，签名上看不出来）、互斥静默忽略（与 WithLogRegistry 同时声明时后者被丢）、双真相源（yaml 写 loki、工厂硬编码 zap，配置静默失效的幽灵换个形态回来）。

**个人项目的裁定**：逃生舱口子不为假想的用户保留——有需要实现一个 provider 注册即可，注册表就是扩展点本身。将来真需要，加回是纯增量；留着不用则持续付文档、测试、认知三重税。

### 为什么 AppKit 的壳不能下沉到 bootstrap？

技术上可以，三个理由说不行：

1. **nil 语义相反是本质冲突**：机制层 fail-fast 是直用用户的契约，策略层回退是生命周期义务——同一个函数签名装不下两种语义，必然变成带开关的分支迷宫；
2. **deco 是用户面概念**：`bslog.Option` 来自 AppKit 的 Option，下沉意味着通用注册表 API 要长出"仅对某类型生效的额外参数"——机制被上层策略打了洞；
3. **壳反正必须有**：阶段 A 回退无论怎么设计都消不掉，把特例留在壳里，比让机制长出策略参数便宜。

这与 2026-08-24"AppKit 不持有日志"的拍板一脉相承：AppKit 只持有装配策略，后端实现与通用构造机制都不在它手里。

### 为什么默认路径的失败是教学报错？

fail-fast 修复静默缺陷的充分条件是**报错可行动**。"not registered (registered: [slog])"式机械错误只告诉用户"不行"；教学错误直接给出三行修复代码，把"切到注册表路径"的知识当场交付。个人项目的依赖哲学：默认给你零依赖的 80%（slog 覆盖绝大多数场景），剩下 20% 一次性教学，不预付 5 个 SDK 的依赖树。

### 被放弃的方案

| 方案 | 放弃原因 |
| --- | --- |
| `WithLoggerFactory` 全接管工厂（v0.3.0 曾有） | 与 LoggerProvider 签名相同、无不可替代能力；带走 nil 隐式契约 / 互斥静默 / 双真相源三债 |
| 默认内置全量注册表（v0.3.0 曾有） | 修复静默失效不需要全量预注册；五后端 SDK 全量捆绑进默认依赖树，代价与收益不成比例 |
| 默认内联小注册表 [slog] | 表里 slog 项被 deco 特例截走、查表永远到不了——"注册了但没人查"的自欺项 |
| nop 契约类型（`type: nop`，v0.3.0 曾有） | 零业务消费；"完全静默"场景虚无——`level: error` 覆盖且保留 Error 线索，真零日志场景裸用 log 包全局默认本来就是 nop |
| 归一为单链路（bootstrap 全权） | nil 语义冲突装不下；deco 下沉污染通用注册表；阶段 A 壳消不掉 |
| build tags 条件编译内置名单 | tag 组合爆炸、CI 矩阵复杂、违反默认全功能惯例 |
| 阶段 A 直接跳过（nop 到阶段 B） | 契约装载是用户代码（ConfigProvider 连远端），窗口内日志静默丢弃 |
| `filter_keys` 各后端自行包装 | 责任点随分派路径数增长；出口统一 + 视图恒等直通是结构性保证 |

### 诚实的代价清单

- 默认路径用远端后端需要三行注册代码——与 database/cache/ai/broker 全家同一纪律，日志不再特殊；
- 两条路径零共享意味着脱敏等横切关注点需在两处出口各自保证（已有结构性机制 + 测试钉死，但新增横切能力时要多想一层）；
- `type: nop` 旧配置升级 v0.4.0 后 fail-fast（解析为未知名）——好于静默，但仍是行为变更。

---

## 兼容性

**v0.4.0 为破坏性收缩**（个人项目、消费者已查证仅 bald-admin/_example 且零引用，实际破坏面为零）：

| 删除项 | 迁移 |
| --- | --- |
| `appkit.WithLoggerFactory` / `LoggerFactory` | 自定义构造逻辑包成 provider + `WithLogRegistry` |
| `bootstrap.NewBuiltinLogRegistry` / `RegisterBuiltinLogProviders` | 按需自建 `NewLogRegistry` + `MustRegister` |
| `bootstrap.NopLoggerProvider` | 无消费场景；需静默用 `level: error` 或裸用 log 包默认 |
| `log.NewNop()` | 同上（`nopLogger` 内部类型保留，仍为全局默认） |
| 契约 `type: nop`（proto NOP=15） | 无消费者；配置残留会在启动期 fail-fast |

保留项：`WithLogRegistry`、`WithLogDecorators`、两阶段生命周期、热更新、脱敏语义全部不变。

---

## 实现与过渡

- [x] `resolveLoggerFactory` 两级分发 + 默认路径纯函数四分派（教学报错）；
- [x] 删除 `WithLoggerFactory` / `LoggerFactory` / `logFac` 字段及互斥判定；
- [x] 删除 `bootstrap/log_builtin.go`（内置全量注册表两 API）与五后端 contract import——bootstrap 对五后端 module 的依赖退场；
- [x] 删除 `NopLoggerProvider`、`log.NewNop()` 导出、契约 `NOP = 15`（proto + buf regenerate）；
- [x] 测试改写：`TestResolveLoggerFactory` 收缩为两级；countingFactory 生命周期测试改注册表 stub provider；默认路径 fail-fast 用例断言教学错误内容；
- [x] 文档同步：本文 / 《Bald 日志设计》/《FromBootstrap 约定装配》/《框架契约总览》/ README。

验证：log / bconf / bootstrap / 根 module CI（build + vet + test -short）全绿；`go mod tidy` 确认五后端依赖从 bootstrap go.mod 退场。

---

## 附录：演进记录——同一天的膨胀与收缩（诚实版）

### 时间线

- **2026-09-13 上午**：为修 `logger.type` 静默失效缺陷，落地三级工厂 + 内置全量注册表（七后端预注册）+ nop 契约类型，发 v0.3.0（9 tag）。
- **同日下午**：复盘两个问题——① 全接管工厂是否必要；② 依赖膨胀（bootstrap 拖进五后端 SDK 全家）。逐层收缩，最终删除 5 个 API + 1 个契约枚举 + 1 个整文件，定为两条路径形态（v0.4.0）。

### 病根与教训

**捆绑决策**：上午把"修复静默失效"和"零代码用远端后端"两个目标打包在"内置全量"一个方案里。拆开各自验代价后发现：前者只需 fail-fast（教学报错即可达成），后者的代价是依赖树全量捆绑——不成比例。

**顺手搭车**：nop 是补"契约能力完整性"时顺手加的（与全量注册同批），零需求驱动。删除评估时全工作区检索零业务消费；"完全静默"场景经论证虚无（`level: error` 覆盖且更优——保留 Error 线索）。

**自欺项**：收缩到"默认小注册表 [slog]"时仍留了一个假注册项——四分派里所有 slog 路径被 deco 特例截走，查表永远到不了 slog。最终连表一起删，默认路径改纯函数。

**教训**：捆绑决策的目标必须拆开各自验代价；"注册了但没人查"的 API 与零消费的入口项同罪；个人项目不为假想用户预付复杂度。

### 历史锚点

- **2026-08-24**：AppKit 不持有日志实现（编排层零全局副作用 / 字段边界 / 多实例互扰三理由）——分层前提。
- **2026-09-06**：FromBootstrap 装配路径落地，两阶段日志内化。
- **2026-09-13**：内置全量 → 同日收缩为两条路径（本文形态）。

关联文档：`Bald 日志设计.md`（日志体系全景：契约层/适配器/后端 module 布局——本文的下游机制）、`AppKit FromBootstrap 约定装配.md`（装配全景速查）、`应用框架设计.md`（AppKit 生命周期）。
