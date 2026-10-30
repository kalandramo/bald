# AppKit 日志装配设计：两阶段引导 + logger 段 Provider

> Author(s): bald 团队
>
> Last updated: 2026-10-30
>
> Discussion at: 源码 `pkg/appkit/standard.go`、`pkg/appkit/bootstrap.go`（`loggerSource`）
>
> Status: Accepted

## 摘要

日志是唯一一个**必须在契约装载之前就可用**的组件：配置源可能连远端、要认证、要重试，这段时间的失败必须看得见。

因此 AppKit 对日志走一条**两阶段通道**（`loggerSource`）：

| 阶段 | 输入 | 行为 |
|---|---|---|
| A（构造期） | `Config == nil` | 调用 logger Provider 的 `Build`，产出引导 logger（内置 Provider 回退默认 bslog） |
| B（契约装载 + 校验后） | 完整契约 | 再次 `Build`，按 `logger.backends` 重建；旧 logger 的 cleanup 冲刷后释放 |
| 热更 | 候选契约 | 同阶段 B，串行执行（`ReloadReconcile`） |

日志 Provider 与其它段一样走 Provider 模型：内置 `StandardLoggerProvider`（`std.Standard()` 成员）与第三方 logger Provider 同通道，段路径末者胜。**默认路径零代码零配置即有可见日志；不支持的后端类型 fail-fast，错误信息直接给出接入方式。**

## 设计

### logger 段节点

`StandardLoggerProvider.Plan` 返回单个节点：

```go
Node{
	Key:        "logger",
	Segments:   []string{"logger"},
	Phase:      PhaseBootstrap,
	Priority:   PriorityLogger,       // 阶段 B 内最先
	Reload:     ReloadReconcile,      // 热更新按期望态重建
	EffectName: "appkit:bootstrap-logger",
	Origin:     OriginStd,
}
```

节点**非 Opaque**：期望态完全由 `logger` 契约段派生（后端列表 / 过滤键），`--plan` 能看到「装配什么」；只有后端构造细节属于 Provider 内部实现。

### 内置 Provider 的三条分派

`stdLoggerProvider.Build`（`standard.go`）按契约分派：

| 条件 | 行为 |
|---|---|
| `logger` 段缺失（`Config == nil` 或配置未写） | 返回默认 bslog（`LogOptions(nil)`，stdout + info） |
| `backends` 空清单 | fail-fast：至少声明一个后端 |
| 逐项 `type == "slog"` | `LogOptions` 映射 + `bslog.NewWithCleanup`；多项经 `MultiLogger` 合并，单项直通 |
| 逐项其它 type | fail-fast 教学报错 |

教学报错直接给出修复方式：

```text
appkit: logger.backends[0] type "loki": default path supports only "slog";
  remote/custom backends need a logger provider:
    appkit.MustServe(ctx, std.Standard(), myLoggerProvider()) // Segments()=["logger"]
```

**为什么默认只支持 slog**：远端后端（loki/aliyun/tencent/sentry/charm）各自独立 module，内置全量支持会把五个 SDK 塞进每个用户的依赖树。默认路径的依赖增量因此严格为零（bslog 本就是 bootstrap 的直接依赖）。

### 两阶段：同一个 nil 的两种含义

- **阶段 A**（`cfg == nil`）：契约还没装载。框架必须持有一个能用的 logger——否则配置源重试的 30 秒里终端一个字不出。Provider 的实现应在此分支返回可用默认值。
- **阶段 B / 热更**（`cfg != nil`）：契约已校验，按声明重建。`loggerSource` 用 `curCleanup` 原子换槽：新 logger 先接管全局句柄，再冲刷旧后端（`rebuildLogger`）——带缓冲后端（loki 尾批）不因热切换丢日志。

启动链任何一步失败，全局句柄恢复装配前的值——写全局被当作可逆效应处理。

### 脱敏只有一处包装

`filter_keys` 要求全部后端恰好包一层 `log.FilterLogger`。内置 Provider 在出口统一包一次：

```go
return log.NewFilterLogger(out, l.GetFilterKeys()...), cleanup, nil
```

provider 逐项只见单个 `Backend`（proto 消息没有 `filter_keys` 字段），子项天然不脱敏——**包装点越少，越不可能漏掉或重复**。

### 第三方 logger Provider

自定义 Provider 只需实现 `Segments() = []string{"logger"}`，`Build` 返回 `log.Logger`。与内置 Provider 的差异由实现自己负责（例如远端后端的连接生命周期放进 cleanup）。`stdLoggerProvider` 是参考实现，也是 `LogOptions` 映射的唯一消费方。

## 理由与取舍

### 为什么日志不参与普通 Provider 的一次性 Build

因为契约装载本身需要日志。若日志只在阶段 B 构造，启动期的配置源故障将不可观测。`loggerSource` 显式承认这一点：它是引擎里唯一有「阶段 A 调用点」的通道，其余段只在契约可用后 Build。

### 为什么 `Build` 必须并发安全

阶段 B 与热更新可能并发进入（配置 watch 在独立 goroutine）。Provider 的 `Build` 不得持有共享可变状态，也不能触碰 `App`（`scope.App` 恒为 nil）。

### 被放弃的方案

| 方案 | 原因 |
|---|---|
| `WithLoggerFactory` 全接管工厂 | 与 Provider 签名等价，本质是「不挂名字的 provider」——`Segments()=["logger"]` 即等价表达，多一条路径只多一份文档与测试 |
| 默认内置全量后端注册表 | 五个 SDK 进默认依赖树；修复「配置不生效」只需 fail-fast，不需要预注册 |
| 阶段 A 用 nop logger | 契约装载窗口内日志静默丢弃，正是最需要可见性的时刻 |
| `filter_keys` 各后端自行包装 | 责任点随后端数增长；出口统一包装是结构性保证 |

## 兼容性

- `logger.backends` 的 `slog` 项开箱即用；其它 type 需要 logger Provider（零代码默认路径会 fail-fast 并给出接入片段）。
- 自定义 Provider 独占 logger 段后，内置 Provider 的 `slog` 分派不再生效——这是段路径覆盖的预期语义。

## 实现与验证

- `pkg/appkit` 测试覆盖：阶段 A 回退、空 backends fail-fast、slog 项直构/多项合并、非 slog 项教学报错、热更新重建与坏配置保留旧 logger。
- 验证命令：`GOFLAGS=-mod=readonly go test ./pkg/appkit/...`。

## 附录：关联文档

- [Bald 日志设计](./Bald%20日志设计.md)：日志体系全景（契约层 / 适配器 / 后端 module 布局）
- [Bald 声明式装配设计](./Bald%20声明式装配设计.md)：Provider 模型、阶段、`ReloadReconcile`
- [应用框架设计](./应用框架设计.md)：AppKit 生命周期与停机序
