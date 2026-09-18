# Bald 熔断设计：契约只留三态与成对上报，四种算法各自独立 module

> Author(s): bald 团队
>
> Last updated: 2026-09-18
>
> Discussion at: 源码 `circuitbreaker/{circuitbreaker.go,circuitbreaker_test.go}` 与四个实现 `circuitbreaker/{hystrix,sentinel,sres,vegas}/`；对照 `retry/retry.go`（组合位置）、`ratelimit/ratelimit.go`（姊妹契约的接口拆分先例）
>
> Status: Accepted（契约与四实现已落地；**全仓零调用点**。审查发现的三个行为缺口——`hystrix` 半开态永久锁死、`vegas` 开态经接口不可恢复、`sentinel` entry 并发泄漏——**已于 2026-09-18 全部修复**，登记与文档补齐，见「兼容性」与「实现与过渡」）

## 摘要

`circuitbreaker` 是 bald 的熔断契约包。它定义一件事：**这次请求该不该发出去**。为此它留了一个三态枚举（`StateClosed`/`StateOpen`/`StateHalfOpen`）、一个哨兵（`ErrCircuitOpen`），以及一个六方法接口：`Allow()`（闸门）、`MarkSuccess()`/`MarkFailure()`（成对上报）、`Execute(ctx, fn)`（便捷包装）、`State()`（观测）、`Close()`（释放）。

四个算法各自住在独立 module 里，通过同一接口挂上去：

| 实现 | 定位 | 依赖 | 测试 |
|---|---|---|---|
| `circuitbreaker/sres` | Google SRE 概率式放行，**平滑降级** | 零第三方 | 14 |
| `circuitbreaker/vegas` | 按 RTT 膨胀提前侦测退化 | 零第三方 | 18 |
| `circuitbreaker/hystrix` | Netflix Hystrix 式阈值 + 睡窗 | 零第三方 | **0** |
| `circuitbreaker/sentinel` | Alibaba Sentinel 桥接，规则交给 Sentinel | `sentinel-golang v1.0.4` | **0** |

我们做了三个承诺：

1. **契约极薄**。`circuitbreaker` 根 module 的 `go.mod` 只有 `module` 与 `go` 两行，与 `health`、`retry`、`ratelimit`、`registry` 同级——它是零件，不是框架。
2. **算法可插拔且互不感知**。`sres` 不知道 `vegas` 存在，`vegas` 也不 import 任何熔断库；换算法只改构造那一行。
3. **「放行即欠一次上报」是契约写明的义务**。`Allow()` 成功返回后，调用方**必须**恰好调用一次 `MarkSuccess` 或 `MarkFailure`——这条义务是四个实现共同的正确性前提，也是它们共同的失效点（见「兼容性」）。

这个 module 是 `da4d533`（六模块批量移植）搬进来的，**全仓零 import**——没有任何一处代码在用它。2026-09-18 审查补齐了设计文档、Taskfile 任务与根 README 登记，并修复了三个行为缺口；契约包注释的 `go-wind` 残留（`circuitbreaker.go:2`）也一并订正为 `bald`。

## 背景与动机

### 三个正交的容错零件，`retry` 与 `ratelimit` 已就位，熔断缺席

`retry`（重试）、`ratelimit`（限流）、`circuitbreaker`（熔断）是同一族的三个零件，分别回答三个不同的问题：

| 零件 | 回答的问题 | 时间尺度 |
|---|---|---|
| `ratelimit` | 这次请求**现在**该不该放行（保护自己） | 毫秒 |
| `circuitbreaker` | 下游**整体**还值不值得试（保护下游 + 快速失败） | 秒 |
| `retry` | 这次失败值不值得**再试一次** | 秒 |

三者刻意互不认识——组合顺序是**语义问题**不是 API 问题，判据留给调用方（`retry` 文档已论述：重试放熔断内层则一次 attempt = 一个熔断样本，放外层则熔断统计的是「逻辑请求」）。熔断缺席时，这套组合缺了中间那一环。

### 契约层想表达熔断，但无处可写

bconf 的配置契约里与熔断相关的字段是**零个**——`grep` 全仓 `GetCircuitBreaker`/`circuitbreaker` 只命中这个 module 自身。对比 `ratelimit` 至少在 `server.proto:69` 有 `rate_limit` 三字段（虽无消费者），熔断连字段都没声明。于是「配了熔断就生效」这件事，今天从契约层就无从表达。

### 一句话定性

**契约形状完整、四实现可用、四个实现均有测试（2026-09-18 补齐 hystrix/sentinel）；审查发现并修复了三个行为缺口，全仓零调用点（尚未接入）。**

## 设计

### 契约：三态、一哨兵、六方法

```go
type State int

const (
	StateClosed   State = iota // 健康，全部放行（默认态）
	StateOpen                  // 已跳闸，立即以 ErrCircuitOpen 拒绝
	StateHalfOpen              // 试探中，只放行有限探针请求
)

func (s State) String() string // "closed" / "open" / "half-open" / "unknown"

var ErrCircuitOpen = errors.New("circuitbreaker: circuit is open")
```

（`circuitbreaker.go:15-49`）

```go
type CircuitBreaker interface {
	Allow() error
	MarkSuccess()
	MarkFailure()
	Execute(ctx context.Context, fn func() error) error
	State() State
	Close() error
}
```

（`circuitbreaker.go:52-80`）

**`Allow` 与 `Mark*` 是成对的**——这是契约最核心也最容易违反的一条，接口注释（`circuitbreaker.go:55-69`）把它写死了三遍：

- `Allow()` 返回 `ErrCircuitOpen` 时，调用方**不应**调用 `Mark*`（闸门没开，没有可上报的结果）；
- `Allow()` 返回 nil 时，调用方**必须**恰好调用一次 `MarkSuccess` 或 `MarkFailure`。

为什么不用「`Allow` 返回一个 token，`Mark` 消费它」这种类型安全的形状？因为那会改掉 `Allow() error` 的签名，而四个实现都建立在这个签名上。当前的折中是：**义务写在注释里，靠 `Execute` 兜住**——`Execute` 内部自动配对（`sres.go:180`、`vegas.go:181`、`hystrix.go:225`、`sentinel.go:149` 四处同构）：

```go
func (b *Breaker) Execute(ctx context.Context, fn func() error) error {
	if err := b.Allow(); err != nil {
		return err
	}
	err := fn()
	if err != nil {
		b.MarkFailure()
		return err
	}
	b.MarkSuccess()
	return nil
}
```

**边界**：`Execute` 只保证自己配对，管不了手写 `Allow`+`Mark*` 的调用方。漏调 `MarkFailure` 会让失败不被计入（熔断永不触发）；漏调 `MarkSuccess` 在 `hystrix` 上更严重——半开态的 `halfOpenIn` 标志永不复位（见「兼容性」第 1 条）。

**`Execute` 的 `fn` 不收 ctx**：签名是 `fn func() error` 而不是 `func(ctx) error`——与 `retry.Do` 的 `func(ctx) error` **不一致**。理由是熔断不做等待（不像重试要 sleep），`fn` 里的取消需求由闭包捕获外层 ctx 即可。代价是同一族的两个零件在这一点上形状不同，调用方要记两套。

### `State()` 的语义因实现而异，且没有一个实现真正「三态」

契约定义了三个状态，但四个实现**没有一个**完整走完三态机——这是设计上最值得展开的一处：

| 实现 | Closed | Open | HalfOpen | 实际状态机 |
|---|---|---|---|---|
| `sres` | ✅ | ✅ | ✅（派生） | **无状态机**——每次 `State()` 由接受率现算，见下 |
| `vegas` | ✅ | ✅ | ✅ | 真三态，但 Open→HalfOpen 需 `RecordLatency` 触发 |
| `hystrix` | ✅ | ✅ | ✅ | 真三态 + 睡窗，但转移有缺陷（见兼容性） |
| `sentinel` | ✅ | ✅ | ❌ **永不返回** | 二态投影——`State()` 只查规则是否存在 |

`sres` 的 `State()`（`sres.go:198-216`）是**纯函数式的**：它不读任何状态字段，而是拿当前窗口的 `requests`/`errors` 现算接受率，`accept <= 0` 报 Open、`accept < 1` 报 HalfOpen、否则 Closed。这带来一个反直觉的性质——**同一个 breaker 连续调两次 `State()`，若中间没有新样本，结果稳定；但一旦窗口旋转（旧桶过期），`State()` 会在没有任何请求的情况下自己变**。这与其他三个实现「状态由事件驱动」的直觉不同。

`sentinel` 的 `State()`（`sentinel.go:172-190`）**永远不返回 HalfOpen**：它先查规则是否配置（无规则即 Closed），再发一次探针 Entry，被 `BlockTypeCircuitBreaking` 拦则报 Open，否则 Closed。HalfOpen 在 Sentinel 内部是存在的，但没有公开 API 能读到——适配器只能投影成二态。

**为什么不在契约层统一**：三态是**能力上限**而非**义务**。`sres` 的概率模型本质上是连续的，硬塞进三态只能取阈值近似（它已经这么做了）；`sentinel` 的状态在别人手里，读不到就是读不到。契约给的是「你能表达多少」，不是「你必须表达全部」。调用方依赖 `State() == StateHalfOpen` 做判断是不可移植的——这条要写进使用须知。

### 实现一：`sres`——概率式平滑降级（零依赖，14 测试）

```go
cb := sres.New(sres.WithK(2), sres.WithWindow(10*time.Second))
defer cb.Close()

err := cb.Execute(ctx, func() error { return downstreamCall() })
```

公式（`sres.go:127-152`，Google SRE 书第 22 章）：

```
accept = max(0, (requests - K * errors) / (requests + 1))
```

`K` 是灵敏度：`K=2` 时错误率超过 50% 开始拒绝，`K=1` 时超过 100%（永不触发），`K=0.5` 时超过 200%（宽松）。`accept >= 1` 直接放行；否则**按 `accept` 概率放行**（`rand.Float64() < accept`）。

**这是四个实现里唯一没有硬跳闸的**——错误率上升时接受率平滑下滑，没有「open 的那一刻」。它把三态当作接受率的粗粒度投影：`accept <= 0` → Open，`0 < accept < 1` → HalfOpen，`accept >= 1` → Closed。

**代价（测试自己承认）**：`TestExecute_AllSuccessNotThrottled`（`sres_test.go:139`）断言「全成功场景下至少 90% 放行」而不是 100%——因为 `accept = requests/(requests+1)` **永远到不了 1**，恒有极小比例被拒。这是概率模型的固有性质，不是 bug，但调用方若期望「全成功 = 全放行」会意外。

### 实现二：`vegas`——按 RTT 膨胀提前侦测（零依赖，18 测试）

```go
cb := vegas.New(vegas.WithAlpha(0.5), vegas.WithBeta(0.3))
err := cb.Execute(ctx, func() error {
	start := time.Now()
	err := downstreamCall()
	cb.RecordLatency(time.Since(start))   // Vegas 的主要输入
	return err
})
```

借 TCP Vegas 拥塞控制的思路：`baseRTT` 是最小观测延迟（健康基线），`currentRTT` 是 0.875/0.125 指数平滑的近期延迟，`inflation = (currentRTT - baseRTT) / baseRTT`。`inflation > alpha` 退化、`inflation < beta` 恢复——`alpha > beta` 构成迟滞，防止抖动。

**它看的是「变慢」而不是「出错」**——下游还没开始报错、只是响应变慢时就能提前降级。这是它相对 `hystrix`/`sres` 的差异化价值。

**边界（三条）**：

1. `MarkSuccess()` 走的是 `RecordLatency(0)`（`vegas.go:145-148`），而 `0 < minRTT(1ms)` 会被离群过滤（`vegas.go:215-218`）丢弃——所以 **`MarkSuccess` 对 Vegas 不喂样本**，源码注释自己承认「MarkSuccess alone records a zero latency which is not useful for Vegas」。要用 Vegas 必须显式调 `RecordLatency`。
2. `minRTT`/`maxRTT`（1ms / 30s）是**写死的常量**，没有 Option（`vegas.go:46-52`）——想调离群边界改不了。
3. ~~**Open 后经接口无法恢复**~~——**已修复（2026-09-18）**：Open/HalfOpen 放行单探针，延迟样本驱动恢复。见「兼容性」第 2 条。

### 实现三：`hystrix`——阈值 + 睡窗（零依赖，0 测试）

```go
cb := hystrix.New(hystrix.WithErrorThreshold(0.5), hystrix.WithSleepWindow(5*time.Second))
```

经典状态机：滑动窗口（默认 10s / 10 桶）里 `requests >= requestVolumeThreshold(20)` 且 `errorRate >= errorThreshold(0.5)` → Open；Open 持续 `sleepWindow(5s)` → HalfOpen；半开放行单个探针，成功回 Closed、失败回 Open 并重置睡窗。

**与 `sres` 的关键差异**：`hystrix` 有**最小请求量门槛**（`requestVolumeThreshold`），低流量时不会因一两个失败误跳闸；`sres` 没有这个门槛，第 1 个请求失败就立刻影响接受率。低流量服务应选 `hystrix`。

**边界**：2026-09-18 前**零测试**——半开转移的缺陷（见「兼容性」第 1 条）正因没有测试才长期未被抓住。现已补 14 个测试，含该缺陷的回归用例。

### 实现四：`sentinel`——桥接，不做规则管理（0 测试）

```go
sentinelapi.InitDefault()
scb.LoadRules([]*scb.Rule{{ Resource: "my-api", Strategy: scb.ErrorRatio, Threshold: 0.5, /* ... */ }})
cb := sentinel.New("my-api")
```

只管 Entry/Exit 生命周期，规则（阈值、策略、熔断、系统保护）全留给调用方。三条 Option：`WithTrafficType`（默认 **`base.Outbound`**——注意与 `ratelimit/sentinel` 的默认 `base.Inbound` **相反**，因为熔断保护的是出站调用）、`WithEntryOptions`、无 `WithWaitInterval`（没有 `Wait` 方法）。

**边界（四条）**：

1. `Close()` 是 no-op（`sentinel.go:191`）——Sentinel 全局单例，适配器无可释放之物。
2. ~~**`Allow()` 持有 entry 到 `Mark*`，同一 breaker 实例不支持并发**~~——**已修复（2026-09-18）**。原实现把 entry 存在单个 `b.entry` 字段，并发 `Allow` 互相覆盖 → 被覆盖的 entry 永不 `Exit`，泄漏 Sentinel 并发计数。改为**待处理 entry 栈**（`pending []*base.SentinelEntry`）：每次 `Allow` 入栈、每次 `Mark*` 出栈，保证「每个 Entry 恰好 Exit 一次」这个真正的不变量在任意交错下成立（计数与顺序无关）。回归测试 `TestEntryPairing_NoLeakUnderConcurrency`（200 并发配对后栈必须为空）。
3. `State()` 的探针 Entry 会**污染统计**——每次调 `State()` 都发一次真实 Entry 再 Exit，计入 Sentinel 的统计窗口。
4. ~~零测试~~——**已补 11 个（2026-09-18）**，含规则装载后跳闸、entry 配对不变量、`Close` no-op 语义。依赖面未变：仍拖 `sentinel-golang v1.0.4` + 18 个 indirect（含 2021 年的 `gopsutil/v3 v3.21.6`）。

### 四实现对照表

| | `sres` | `vegas` | `hystrix` | `sentinel` |
|---|---|---|---|---|
| 契约接口 | `CircuitBreaker` | `CircuitBreaker` | `CircuitBreaker` | `CircuitBreaker` |
| 构造 | `New(opts...) *Breaker` | `New(opts...) *Breaker` | `New(opts...) *Breaker` | `New(resource, opts...) *Breaker` |
| 判决依据 | 错误率（概率） | RTT 膨胀 | 错误率（阈值） | Sentinel 规则 |
| 硬跳闸 | ❌ 无（平滑） | ✅ | ✅ | ✅ |
| 最小请求量门槛 | ❌ | 有 warmup | ✅ `requestVolumeThreshold` | 由规则定 |
| 返回 error | ❌ | ❌ | ❌ | ❌ |
| 非法配置 | 静默忽略 | 静默忽略 | 静默忽略 | 静默忽略 |
| 额外方法 | — | `RecordLatency`/`BaseRTT`/`CurrentRTT`/`Inflation` | — | `AllowEntry`?（无，仅内部 entry） |
| 并发安全 | ✅ | ✅ | ✅ | ❌（`b.entry` 竞态） |
| 第三方依赖 | 无 | 无 | 无 | `sentinel-golang v1.0.4` |
| 测试 | 14 | 18 | **0** | **0** |

这张表是本文档存在的主要理由之一：**同一份契约下的四个实现，判决依据、并发保证、可观测面各不相同，而这些差异必须写在类型与文档上而不是藏在注释里。**

## 理由与取舍

### 我们没选「只有两态」（closed/open）的契约

两态是最常见的熔断形状（`gobreaker` 等），实现更简单。我们保留三态，因为 **half-open 是一个语义上真实的阶段**——「下游可能恢复了，但我不确定，先放一个探针试试」。把它压成两态会丢掉这个语义，调用方无法表达「正在试探」。

**代价我们承认**：四个实现没有一个完整兑现三态（`sres` 是派生的、`sentinel` 永不返回 half-open），契约的承诺大于实现的能力。

### 我们没把 `MarkSuccess`/`MarkFailure` 合成 `Mark(err error)`

合成一个方法看似更简洁，但会把「成功」与「失败」的判定塞进参数——而 `err != nil` 未必等于「该记为失败」（比如 `context.Canceled` 可能不该计入）。两个方法让调用方显式决定语义。

**代价**：多一个方法，且配对义务翻倍（`Allow` + 二选一 `Mark*`）。

### 我们没让 `Execute` 的 `fn` 收 ctx

见「设计」——与 `retry.Do` 不一致是刻意接受的代价：熔断不等待，`fn` 的取消由闭包捕获。要统一得改 `retry` 或改这里，两边都有破坏面，暂无消费者时不动。

### 我们没在 `sres` 上加最小请求量门槛

`hystrix` 有 `requestVolumeThreshold`，`sres` 没有。这是**刻意的差异**而非遗漏：SRE 公式本身用 `+1` 平滑了低样本量的影响（`requests=1, errors=1` 时 `accept = (1-K)/(2)`，K=2 时为 -0.5 → 0），加门槛会与公式的平滑意图冲突。需要门槛的场景请用 `hystrix`。

### 我们没让 `vegas` 的 `minRTT`/`maxRTT` 可配

它们是离群过滤器，不是业务旋钮。暴露成 Option 会诱导调用方调它们来「修」延迟分布问题——而那应该由 `alpha`/`beta` 表达。

**代价**：见「兼容性」第 2 条——`minRTT` 过滤掉的正是 `MarkSuccess` 喂的零延迟，这个耦合是缺口的一半。

### 我们没在契约里提供「熔断 + 重试 + 限流」的组合门面

三者的顺序是语义问题（见「背景」），框架不该替调用方拍板。这一点与 `retry` 文档的论述一致。

## 兼容性

### 纯新增，零破坏

`circuitbreaker` 是新 module（`da4d533`）。全仓搜 `kalandramo/bald/circuitbreaker` **只命中它自己的五个 `go.mod`**，没有任何现有调用方受影响，不需要迁移。

### 但四件事必须说清（三件是审查实证的行为缺口，均已于 2026-09-18 修复）

> **审查方法说明**：以下第 1、2 条不是读代码推断，是**探针实测**——临时测试文件写入后运行、读输出、删除（已清理）。原始观测值见各条内引。

#### 1. 【高】`hystrix` 半开态永久锁死（已修复）

`Allow()`（`hystrix.go:149-171`）在 Open→HalfOpen 转移时**先设 `halfOpenIn = true`，随即 `fallthrough` 到 HalfOpen 分支，被自己刚设的标志拒绝**：

```go
case circuitbreaker.StateOpen:
	if now.Sub(b.openedAt) >= b.cfg.sleepWindow {
		b.state = circuitbreaker.StateHalfOpen
		b.halfOpenIn = true          // ← 设 true
	} else {
		return circuitbreaker.ErrCircuitOpen
	}
	fallthrough                      // ← 落入下面
case circuitbreaker.StateHalfOpen:
	if b.halfOpenIn {                // ← 立刻读到 true → 拒绝
		return circuitbreaker.ErrCircuitOpen
	}
	b.halfOpenIn = true
}
```

探针实测（`sleepWindow=50ms`，持续失败触发 Open 后等 80ms）：

```
after failures: state=open
first_allow_after_sleep:  err=circuitbreaker: circuit is open  state=half-open
second_allow:             err=circuitbreaker: circuit is open  state=half-open
third_allow:              err=circuitbreaker: circuit is open  state=half-open
```

`halfOpenIn` 只在 `MarkSuccess`/`MarkFailure` 里复位（`hystrix.go:182-202`），而探针请求被拒 → 调用方**不会**调 `Mark*`（契约规定 `Allow` 失败时不应调）→ 标志永不复位。走 `Execute`（真实中间件路径，内部不调 `State()`）实测 100 次迭代无一恢复：

```
PROBE2_EXECUTE_RECOVERY=false final=half-open
```

**但**若调用方先调 `State()`（`hystrix.go:241-253` 的 lazy transition 只改 `state` 不设 `halfOpenIn`），随后的 `Allow` 反而放行：

```
state_before_allow=half-open
allow_after_state_first: err=<nil>
```

同一状态机在两条路径上行为相反——这坐实了转移逻辑有缺陷，而非某种刻意语义。

**影响**：一旦 `hystrix` 跳闸且调用方走 `Execute`/`Allow`（不主动轮询 `State()`），下游恢复后**熔断器永不恢复**——所有请求被永久拒绝。这是熔断器最严重的失效模式（比不熔断更糟：不熔断至少会重试到下游恢复）。

**修复（2026-09-18）**：Open 分支不再预置 `halfOpenIn`——它只做状态转移，`halfOpenIn` 的置位统一交给 HalfOpen 分支（唯一置位点）。补 4 个回归测试（`TestAllow_HalfOpenAdmitsProbe`、`TestExecute_RecoversAfterDownstreamHeals`、`TestMarkFailure_HalfOpenReopens`、`TestStateAndAllow_TransitionConsistently`）；回滚修复后这 4 个测试全部变红，确认它们真能捕获该缺陷。

#### 2. 【中】`vegas` 开态后经接口不可恢复（已修复）

`Allow()` 在 Open 时直接拒绝（`vegas.go:127-143`）→ `Execute` 的 `fn` 不执行 → 无新延迟样本；而 `MarkSuccess()` 喂的 `RecordLatency(0)` 被 `minRTT` 过滤（见「设计」实现二）。恢复的唯一入口是**接口外的** `RecordLatency`——探针实测：

```
after degradation: state=open inflation=3.927
PROBE_INTERFACE_RECOVERY=false  final_state=open inflation=3.927   （200 次 Allow/Mark/Execute）
PROBE_RECORDLATENCY_RECOVERY: state=closed inflation=0.001          （60 次 RecordLatency）
```

**这与 `ratelimit` 的 `InflightLimiter` 缺口同构**：能力（这里是「喂延迟样本」）只存在于具体类型上，持有 `circuitbreaker.CircuitBreaker` 静态类型的调用方够不到。`ratelimit` 的解法是拆一个 `InflightLimiter` 子接口；`vegas` 可以照做（如 `LatencyReporter{ RecordLatency(time.Duration) }`），或让 `MarkSuccess` 不再走零延迟。

**影响**：用 Vegas 且通过接口调用时，一次延迟尖峰会导致熔断器永久 open——除非调用方恰好持有 `*vegas.Breaker` 并记得调 `RecordLatency`。

**修复（2026-09-18）**：Open/HalfOpen 态放行**单个探针请求**（新增 `probeInFlight` 标志，`Allow` 在 Open/HalfOpen 时门控为一次一个），探针的延迟样本（经 `Execute` 自动 `RecordLatency`）驱动恢复。探针槽位在 `RecordLatency`/`MarkFailure` 两条完成路径上均复位——这是从 `hystrix` 缺陷吸取的教训：**槽位必须在所有完成路径上释放**，否则重蹈锁死。回归测试 `TestAllow_OpenAdmitsSingleProbe`、`TestExecute_RecoversAfterLatencyHeals`（下游恢复后 39 次 `Execute` 自愈）；回滚后两者变红。

#### 3. `sres` 全成功场景仍会拒掉少量请求

见「设计」实现一——`accept` 渐近于 1 但永不到达。这是概率模型的固有性质，测试已把它固化为「≥90% 放行」。

#### 4. 【中】`sentinel` 实例不支持并发（已修复）

见「设计」实现四第 2 条——原 `b.entry` 是单字段，并发 `Allow` 互相覆盖 → 被覆盖的 entry 永不 `Exit`（泄漏 Sentinel 并发计数）。契约要求并发安全（`circuitbreaker.go:54`），原实现不满足。**修复（2026-09-18）**：改为待处理 entry 栈，保证「每个 Entry 恰好 Exit 一次」在任意交错下成立。回归测试 `TestEntryPairing_NoLeakUnderConcurrency`；回滚后泄漏 100 个 entry（并发数一半），确认测试有效。

### 迁移策略：不强制迁移

没有存量调用方。要落地的是**接入**：把某个 breaker 挂到出站调用（HTTP client / gRPC client / DB 查询）上。缺口 1、2、4 已修，接入前的阻塞项已清除。

## 实现与过渡

### 已落地

`circuitbreaker/` 根 module（`circuitbreaker.go` + 测试）+ 四实现 module，共 5 个 module、8 个 Go 文件、1742 行。引入于 `da4d533`（六模块批量移植）。

测试 78 个：契约 3（`State.String` 四值、`ErrCircuitOpen` 自比较、dummy 实现的接口形状）+ `sres` 14 + `vegas` 20 + `hystrix` 14 + `sentinel` 11。**2026-09-18 补齐了 `hystrix`（原 0）与 `sentinel`（原 0）**——`hystrix` 是纯本地逻辑，测试从半开转移缺陷的回归用例起步；`sentinel` 需全局初始化与规则装载，测试用 `sync.Once` 初始化 + 独立资源名隔离。

### 登记工作一件没做

1. **`Taskfile.yml` 零处 `circuitbreaker`**。嵌套 module 不在根 `go build ./...` 范围内，故本地 `task verify` **完全跑不到这五个 module**。对照 `retry`/`ratelimit`/`broker` 已于 2026-09-18 补齐（`*-verify` 任务），`circuitbreaker` 是同类缺口的遗留。
2. **根 `README.md` 零处 `circuitbreaker`**，架构树未登记。
3. **`docs/devel/zh-CN/README.md` 收录本文**（2026-09-18 随本文补入索引）。

**CI 不需要改**：`.github/workflows/ci.yml` 用 `find . -name go.mod` 自动发现全部 module，五个 `circuitbreaker` module 已被自动纳入 build + vet + test。

### 修复与接入顺序（建议）

### 2026-09-18 修复与登记（已执行）

| 项 | 处置 | 验证 |
|---|---|---|
| 1. `hystrix` 半开锁死（缺口 1） | Open 分支不再预置 `halfOpenIn`（唯一置位点归 HalfOpen 分支） | 新增 4 个回归测试；回滚后全红 |
| 2. `vegas` 开态不可恢复（缺口 2） | Open/HalfOpen 放行单探针（`probeInFlight`），槽位在所有完成路径复位 | 新增 2 个测试；回滚后全红 |
| 3. `sentinel` entry 泄漏（缺口 4） | 单字段 → 待处理 entry 栈 | 新增 11 个测试（原 0），含配对不变量；回滚后泄漏 100 |
| 4. 补 `hystrix` 测试 | 从 0 到 14，含半开转移回归 | `go test` 全绿 |
| 5. 契约包注释 `go-wind` → `bald` | 订正并补配对义务说明 | 编译通过 |
| 6. 登记 | Taskfile 补 `circuitbreaker-verify`（5 module 逐 `dir:`）挂 `verify`；根 README 登记 | `task circuitbreaker-verify` 实跑全绿 |

**尚未做（属接入，非修复）**：确定组合顺序（`retry` 在 `circuitbreaker` 内层还是外层）、选定默认实现（低流量用 `hystrix`、需平滑降级用 `sres`、需延迟感知用 `vegas`）。`vegas` 的 `LatencyReporter` 子接口未引入——探针机制已让接口路径可恢复，该子接口不再是必需（见「开放问题」）。

### 验证方式

```bash
task circuitbreaker-verify    # 五 module：build + vet + test（推荐）
# 或逐 module：
cd circuitbreaker && go vet ./... && go test -count=1 ./...          # 契约 3
cd circuitbreaker/sres && go vet ./... && go test -count=1 ./...    # 14
cd circuitbreaker/vegas && go vet ./... && go test -count=1 ./...   # 20
cd circuitbreaker/hystrix && go vet ./... && go test -count=1 ./... # 14
cd circuitbreaker/sentinel && go vet ./... && go test -count=1 ./... # 11
```

## 附录

### 完整 API

| module | 类别 | 导出符号 | 默认值 |
|---|---|---|---|
| `circuitbreaker` | 类型 | `State`（`StateClosed`/`StateOpen`/`StateHalfOpen`） | — |
| | 哨兵 | `ErrCircuitOpen` | — |
| | 接口 | `CircuitBreaker{Allow, MarkSuccess, MarkFailure, Execute, State, Close}` | — |
| `sres` | 构造 | `New(opts ...Option) *Breaker` | — |
| | 配置 | `WithK(float64)` | `2.0` |
| | | `WithWindow(time.Duration)` | `10s` |
| | | `WithBucketCount(int)` | `40` |
| `vegas` | 构造 | `New(opts ...Option) *Breaker` | — |
| | 配置 | `WithAlpha(float64)` | `0.5` |
| | | `WithBeta(float64)` | `0.3` |
| | | `WithWarmupSamples(int)` | `10` |
| | 方法 | `RecordLatency(time.Duration)`、`BaseRTT()`、`CurrentRTT()`、`Inflation()` | — |
| `hystrix` | 构造 | `New(opts ...Option) *Breaker` | — |
| | 配置 | `WithErrorThreshold(float64)` | `0.50` |
| | | `WithRequestVolumeThreshold(int)` | `20` |
| | | `WithSleepWindow(time.Duration)` | `5s` |
| | | `WithWindow(time.Duration)` | `10s` |
| | | `WithBucketCount(int)` | `10` |
| `sentinel` | 构造 | `New(resource string, opts ...Option) *Breaker` | — |
| | 配置 | `WithTrafficType(base.TrafficType)` | `base.Outbound` |
| | | `WithEntryOptions(...sentinelapi.EntryOption)` | — |

### FAQ

**`Allow` 返回 nil 后忘了调 `Mark*` 会怎样？** 失败不会被计入 → 熔断永不触发（对 `hystrix`/`sres` 是「该跳不跳」）；在 `hystrix` 半开态还会让 `halfOpenIn` 不复位、锁死该状态（2026-09-18 修复了转移点的自拒缺陷，但「漏调 Mark*」本身仍是调用方的责任）。`vegas` 与 `sentinel` 同理——漏调会卡住探针槽位 / 泄漏 entry。用 `Execute` 可避免。

**`Execute` 的 `fn` 为什么收不到 ctx？** 见「理由与取舍」。熔断不等待，取消由闭包捕获。

**`sres` 和 `hystrix` 该选哪个？** 高流量、要平滑降级 → `sres`；低流量、需要最小请求量门槛防误判 → `hystrix`。`sres` 没有门槛，第 1 个请求就影响接受率。

**`vegas` 和另外三个有什么本质不同？** 它看**延迟**不看错误——下游变慢但还没报错时就能提前降级。代价是必须显式喂 `RecordLatency`（`MarkSuccess` 喂的零延迟会被 `minRTT` 过滤）。Open 态现已放行单个探针（2026-09-18 修复），探针的延迟样本驱动恢复。

**`sentinel` 的 `State()` 会返回 `StateHalfOpen` 吗？** 不会。Sentinel 的内部状态没有公开 API，适配器只能投影成 Closed/Open 二态。

**`sentinel` 的默认 `TrafficType` 为什么是 `Outbound`？** 熔断保护的是**出站**调用（我方调下游），而 `ratelimit/sentinel` 的默认是 `Inbound`（保护自己收请求）。两者相反是刻意的。

**`Close()` 之后还能用吗？** `sres`/`vegas`/`hystrix` 的 `Close()` 置 `closed=true`，之后 `Allow()` 恒返回 `ErrCircuitOpen`（相当于永久 open）。`sentinel` 的 `Close()` 是 no-op，之后照常工作。

### 开放问题

1. ~~**`hystrix` 半开锁死怎么修？**~~ **已修（2026-09-18）**：转移时不设 `halfOpenIn`，交 HalfOpen 分支统一置位。已补 4 个回归测试。
2. **`vegas` 要不要拆 `LatencyReporter` 子接口？** 探针机制（2026-09-18）已让接口路径可恢复，故**不再是必需**。若要更精细的延迟上报（不经 `Execute`），仍可照 `ratelimit.InflightLimiter` 先例拆子接口；当前不做。
3. **契约要不要统一三态语义？** `sres` 派生、`sentinel` 二态投影——是收紧契约（要求实现真正三态）还是放宽文档（声明三态是能力上限）？当前是后者。
4. ~~**`sentinel` 的并发问题怎么办？**~~ **已修（2026-09-18）**：不改基础契约，改用待处理 entry 栈——「每个 Entry 恰好 Exit 一次」在任意交错下成立。
5. **要不要补 bconf 的熔断契约段？** 当前配置层零字段。加了才能「配了熔断就生效」。
6. **`vegas` 的 `minRTT`/`maxRTT` 要不要暴露成 Option？** 见「理由与取舍」——倾向不暴露，但缺口 2 的修法可能绕不开它。
