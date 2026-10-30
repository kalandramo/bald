# Bald 熔断设计：契约只留三态与成对上报，四种算法各自独立 module

> Author(s): bald 团队
>
> Last updated: 2026-09-18
>
> Discussion at: 源码 `circuitbreaker/{circuitbreaker.go,circuitbreaker_test.go}` 与四个实现 `circuitbreaker/{hystrix,sentinel,sres,vegas}/`；对照 `retry/retry.go`（组合位置）、`ratelimit/ratelimit.go`（姊妹契约的接口拆分先例）
>
> Status: Accepted（契约与四实现已落地；**全仓零调用点**。三个行为边界的正确形态——`hystrix` 半开转移、`vegas` 开态经接口恢复、`sentinel` entry 并发释放——见「边界与不变量」）

## 摘要

`circuitbreaker` 是 bald 的熔断契约包。它定义一件事：**这次请求该不该发出去**。为此它留了一个三态枚举（`StateClosed`/`StateOpen`/`StateHalfOpen`）、一个哨兵（`ErrCircuitOpen`），以及一个六方法接口：`Allow()`（闸门）、`MarkSuccess()`/`MarkFailure()`（成对上报）、`Execute(ctx, fn)`（便捷包装）、`State()`（观测）、`Close()`（释放）。

四个算法各自住在独立 module 里，通过同一接口挂上去：

| 实现 | 定位 | 依赖 | 测试 |
|---|---|---|---|
| `circuitbreaker/sres` | Google SRE 概率式放行，**平滑降级** | 零第三方 | 19 |
| `circuitbreaker/vegas` | 按 RTT 膨胀提前侦测退化 | 零第三方 | 20 |
| `circuitbreaker/hystrix` | Netflix Hystrix 式阈值 + 睡窗 | 零第三方 | 14 |
| `circuitbreaker/sentinel` | Alibaba Sentinel 桥接，规则交给 Sentinel | `sentinel-golang v1.0.4` | 11 |

我们做了三个承诺：

1. **契约极薄**。`circuitbreaker` 根 module 的 `go.mod` 只有 `module` 与 `go` 两行，与 `health`、`retry`、`ratelimit`、`registry` 同级——它是零件，不是框架。
2. **算法可插拔且互不感知**。`sres` 不知道 `vegas` 存在，`vegas` 也不 import 任何熔断库；换算法只改构造那一行。
3. **「放行即欠一次上报」是契约写明的义务**。`Allow()` 成功返回后，调用方**必须**恰好调用一次 `MarkSuccess` 或 `MarkFailure`——这条义务是四个实现共同的正确性前提，也是它们共同的失效点（见「兼容性」）。

这个 module **全仓零 import**——没有任何生产代码在用它，框架能力面先于消费者存在（见《设计判据》J1）。Taskfile 任务、根 README 登记与设计文档齐备；契约包注释使用 `bald`。

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

bconf 的配置契约里与熔断相关的字段是**零个**——`grep` 全仓 `GetCircuitBreaker`/`circuitbreaker` 只命中这个 module 自身。对比 `ratelimit` 至少在 `server.proto` 有 `rate_limit` 三字段（虽无消费者），熔断连字段都没声明。于是「配了熔断就生效」这件事，今天从契约层就无从表达。

### 一句话定性

**契约形状完整、四实现可用、四个实现均有测试，全仓零调用点（尚未接入）。**

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

（`circuitbreaker.go`）

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

（`circuitbreaker.go`）

**`Allow` 与 `Mark*` 是成对的**——这是契约最核心也最容易违反的一条，接口注释（`circuitbreaker.go`）把它写死了三遍：

- `Allow()` 返回 `ErrCircuitOpen` 时，调用方**不应**调用 `Mark*`（闸门没开，没有可上报的结果）；
- `Allow()` 返回 nil 时，调用方**必须**恰好调用一次 `MarkSuccess` 或 `MarkFailure`。

为什么不用「`Allow` 返回一个 token，`Mark` 消费它」这种类型安全的形状？因为那会改掉 `Allow() error` 的签名，而四个实现都建立在这个签名上。当前的折中是：**义务写在注释里，靠 `Execute` 兜住**——`Execute` 内部自动配对（`sres.go`、`vegas.go`、`hystrix.go`、`sentinel.go` 四处同构）：

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
| `hystrix` | ✅ | ✅ | ✅ | 真三态 + 睡窗（半开转移的置位点见「边界与不变量」） |
| `sentinel` | ✅ | ✅ | ❌ **永不返回** | 二态投影——`State()` 只查规则是否存在 |

`sres` 的 `State()` 是**纯函数式的**：它不读任何状态字段，而是拿当前窗口的 `requests`/`errors` 现算接受率，`accept <= 0` 报 Open、`accept < 1` 报 HalfOpen、否则 Closed。这带来一个反直觉的性质——**同一个 breaker 连续调两次 `State()`，若中间没有新样本，结果稳定；但一旦窗口旋转（旧桶过期），`State()` 会在没有任何请求的情况下自己变**。这与其他三个实现「状态由事件驱动」的直觉不同。

`sentinel` 的 `State()` **永远不返回 HalfOpen**：它先查规则是否配置（无规则即 Closed），再发一次探针 Entry，被 `BlockTypeCircuitBreaking` 拦则报 Open，否则 Closed。HalfOpen 在 Sentinel 内部是存在的，但没有公开 API 能读到——适配器只能投影成二态。

**为什么不在契约层统一**：三态是**能力上限**而非**义务**。`sres` 的概率模型本质上是连续的，硬塞进三态只能取阈值近似（它已经这么做了）；`sentinel` 的状态在别人手里，读不到就是读不到。契约给的是「你能表达多少」，不是「你必须表达全部」。调用方依赖 `State() == StateHalfOpen` 做判断是不可移植的——这条要写进使用须知。

### 实现一：`sres`——概率式平滑降级（零依赖，19 测试）

```go
cb := sres.New(sres.WithK(2), sres.WithWindow(10*time.Second))
defer cb.Close()

err := cb.Execute(ctx, func() error { return downstreamCall() })
```

公式（`sres.go`，Google SRE 书第 22 章）：

```
accept = max(0, (requests - K * errors) / (requests + 1))
```

`K` 是灵敏度：`K=2` 时错误率超过 50% 开始拒绝，`K=1` 时超过 100%（永不触发），`K=0.5` 时超过 200%（宽松）。`accept >= 1` 直接放行；否则**按 `accept` 概率放行**（`rand.Float64() < accept`）。

**这是四个实现里唯一没有硬跳闸的**——错误率上升时接受率平滑下滑，没有「open 的那一刻」。它把三态当作接受率的粗粒度投影：`accept <= 0` → Open，`0 < accept < 1` → HalfOpen，`accept >= 1` → Closed。

**代价（测试自己承认）**：`TestExecute_AllSuccessNotThrottled`（`sres_test.go`）断言「全成功场景下至少 90% 放行」而不是 100%——因为 `accept = requests/(requests+1)` **永远到不了 1**，恒有极小比例被拒。这是概率模型的固有性质，不是 bug，但调用方若期望「全成功 = 全放行」会意外。

### 实现二：`vegas`——按 RTT 膨胀提前侦测（零依赖，20 测试）

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

1. `MarkSuccess()` 走的是 `RecordLatency(0)`（`vegas.go`），而 `0 < minRTT(1ms)` 会被离群过滤（`vegas.go`）丢弃——所以 **`MarkSuccess` 对 Vegas 不喂样本**，源码注释自己承认「MarkSuccess alone records a zero latency which is not useful for Vegas」。要用 Vegas 必须显式调 `RecordLatency`。
2. `minRTT`/`maxRTT`（1ms / 30s）是**写死的常量**，没有 Option（`vegas.go`）——想调离群边界改不了。
3. Open/HalfOpen 放行**单个探针**（`probeInFlight`），探针的延迟样本驱动恢复——经接口调用也能自愈，不依赖调用方持有具体类型去调 `RecordLatency`。

### 实现三：`hystrix`——阈值 + 睡窗（零依赖，14 测试）

```go
cb := hystrix.New(hystrix.WithErrorThreshold(0.5), hystrix.WithSleepWindow(5*time.Second))
```

经典状态机：滑动窗口（默认 10s / 10 桶）里 `requests >= requestVolumeThreshold(20)` 且 `errorRate >= errorThreshold(0.5)` → Open；Open 持续 `sleepWindow(5s)` → HalfOpen；半开放行单个探针，成功回 Closed、失败回 Open 并重置睡窗。

**与 `sres` 的关键差异**：`hystrix` 有**最小请求量门槛**（`requestVolumeThreshold`），低流量时不会因一两个失败误跳闸；`sres` 没有这个门槛，第 1 个请求失败就立刻影响接受率。低流量服务应选 `hystrix`。

**边界**：半开转移是这一实现最容易出错的地方（标志位若在 Open 分支预置会被 `fallthrough` 立刻拒绝，而拒绝路径不调 `Mark*`，标志永不复位 → 永久锁死）。`TestAllow_HalfOpenAdmitsProbe` 等 4 个用例把转移的正确行为钉住。

### 实现四：`sentinel`——桥接，不做规则管理（11 测试）

```go
sentinelapi.InitDefault()
scb.LoadRules([]*scb.Rule{{ Resource: "my-api", Strategy: scb.ErrorRatio, Threshold: 0.5, /* ... */ }})
cb := sentinel.New("my-api")
```

只管 Entry/Exit 生命周期，规则（阈值、策略、熔断、系统保护）全留给调用方。三条 Option：`WithTrafficType`（默认 **`base.Outbound`**——注意与 `ratelimit/sentinel` 的默认 `base.Inbound` **相反**，因为熔断保护的是出站调用）、`WithEntryOptions`、无 `WithWaitInterval`（没有 `Wait` 方法）。

**边界（四条）**：

1. `Close()` 是 no-op（`sentinel.go`）——Sentinel 全局单例，适配器无可释放之物。
2. **`Allow()` 与 `Mark*` 支持并发**——entry 存在**待处理 entry 栈**（`pending []*base.SentinelEntry`）里：每次 `Allow` 入栈、每次 `Mark*` 出栈，「每个 Entry 恰好 Exit 一次」在任意交错下成立（计数与顺序无关）。`TestEntryPairing_NoLeakUnderConcurrency`（200 并发配对后栈必须为空）钉住该不变量。
3. `State()` 的探针 Entry 会**污染统计**——每次调 `State()` 都发一次真实 Entry 再 Exit，计入 Sentinel 的统计窗口。
4. 测试 11 个，含规则装载后跳闸、entry 配对不变量、`Close` no-op 语义。依赖面：`sentinel-golang v1.0.4` + 18 个 indirect（含 2021 年的 `gopsutil/v3 v3.21.6`）。

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

## 边界与不变量

### 契约只承诺三态，实现可以只兑现两态

`sres` 与 `vegas` 没有独立的 Open→HalfOpen 计时转移：`sres` 用概率接受度平滑降级（永不 hard open），`vegas` 用延迟样本驱动膨胀/收缩。契约的 `State()` 因此是一个**诊断视图**，不是所有实现共享的精确状态机——调用方不应基于 `State()` 做业务分支。

### 恢复必须经接口可达

熔断器最严重的失效模式不是「不熔断」，而是「熔断后永不恢复」。两条不变量由此而来：

- **`hystrix`**：Open 分支只做状态转移，`halfOpenIn` 的唯一置位点在 HalfOpen 分支。若在转移时预置标志，`fallthrough` 会立刻被自己拒绝，而拒绝路径调用方不会调 `Mark*`，标志永不复位 → 永久锁死。
- **`vegas`**：Open/HalfOpen 态放行**单个探针请求**（`probeInFlight`），探针的延迟样本驱动恢复。槽位必须在 `RecordLatency` 与 `MarkFailure` 两条完成路径上都复位——槽位泄漏等价于锁死。

回归测试：`TestAllow_HalfOpenAdmitsProbe`、`TestExecute_RecoversAfterDownstreamHeals`、`TestMarkFailure_HalfOpenReopens`、`TestStateAndAllow_TransitionConsistently`、`TestAllow_OpenAdmitsSingleProbe`、`TestExecute_RecoversAfterLatencyHeals`。

### `sentinel` 的 entry 必须成对释放

`Allow()` 返回的 entry 必须在 `Mark*` 时 `Exit()` 恰好一次。单字段存 entry 会被并发 `Allow` 互相覆盖，被覆盖的 entry 永不 `Exit`，泄漏 Sentinel 的并发计数。实现用**待处理 entry 栈**：每次 `Allow` 入栈、每次 `Mark*` 出栈，「每个 Entry 恰好 Exit 一次」在任意交错下成立（计数与顺序无关）。回归测试 `TestEntryPairing_NoLeakUnderConcurrency`（200 并发配对后栈必须为空）。

### `sres` 全成功场景仍会拒掉少量请求

见「设计」实现一——`accept` 渐近于 1 但永不到达。这是概率模型的固有性质，测试把它固化为「≥90% 放行」。

## 实现与验证

`circuitbreaker/` 根 module（契约 + 测试）+ 四实现 module，共 5 个 module。测试 67 个：契约 3 + `sres` 19 + `vegas` 20 + `hystrix` 14 + `sentinel` 11。`sentinel` 测试用 `sync.Once` 初始化 + 独立资源名隔离。

登记完整：`Taskfile.yml` 的 `circuitbreaker-verify` 逐 module 挂入 `verify`；根 `README.md` 与本文索引均已收录。嵌套 module 不在根 `go build ./...` 范围内，CI 用 `find . -name go.mod` 自动发现全部 module。

**尚未做（属接入，非修复）**：确定组合顺序（`retry` 在 `circuitbreaker` 内层还是外层）、选定默认实现（低流量用 `hystrix`、需平滑降级用 `sres`、需延迟感知用 `vegas`）。`vegas` 的 `LatencyReporter` 子接口未引入——探针机制已让接口路径可恢复，该子接口不再是必需（见「开放问题」）。

### 验证方式

```bash
task circuitbreaker-verify    # 五 module：build + vet + test（推荐）
# 或逐 module：
cd circuitbreaker && go vet ./... && go test -count=1 ./...          # 契约 3
cd circuitbreaker/sres && go vet ./... && go test -count=1 ./...    # 19
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

**`Allow` 返回 nil 后忘了调 `Mark*` 会怎样？** 失败不会被计入 → 熔断永不触发（对 `hystrix`/`sres` 是「该跳不跳」）；在 `hystrix` 半开态还会让 `halfOpenIn` 不复位、锁死该状态（转移点已不自拒，但「漏调 Mark*」仍是调用方的责任）。`vegas` 与 `sentinel` 同理——漏调会卡住探针槽位 / 泄漏 entry。用 `Execute` 可避免。

**`Execute` 的 `fn` 为什么收不到 ctx？** 见「理由与取舍」。熔断不等待，取消由闭包捕获。

**`sres` 和 `hystrix` 该选哪个？** 高流量、要平滑降级 → `sres`；低流量、需要最小请求量门槛防误判 → `hystrix`。`sres` 没有门槛，第 1 个请求就影响接受率。

**`vegas` 和另外三个有什么本质不同？** 它看**延迟**不看错误——下游变慢但还没报错时就能提前降级。代价是必须显式喂 `RecordLatency`（`MarkSuccess` 喂的零延迟会被 `minRTT` 过滤）。Open 态放行单个探针，探针的延迟样本驱动恢复。

**`sentinel` 的 `State()` 会返回 `StateHalfOpen` 吗？** 不会。Sentinel 的内部状态没有公开 API，适配器只能投影成 Closed/Open 二态。

**`sentinel` 的默认 `TrafficType` 为什么是 `Outbound`？** 熔断保护的是**出站**调用（我方调下游），而 `ratelimit/sentinel` 的默认是 `Inbound`（保护自己收请求）。两者相反是刻意的。

**`Close()` 之后还能用吗？** `sres`/`vegas`/`hystrix` 的 `Close()` 置 `closed=true`，之后 `Allow()` 恒返回 `ErrCircuitOpen`（相当于永久 open）。`sentinel` 的 `Close()` 是 no-op，之后照常工作。

### 开放问题

1. **`vegas` 要不要拆 `LatencyReporter` 子接口？** 探针机制已让接口路径可恢复，故不再是必需。若要更精细的延迟上报（不经 `Execute`），仍可照 `ratelimit.InflightLimiter` 先例拆子接口；当前不做。
2. **契约要不要统一三态语义？** `sres` 派生、`sentinel` 二态投影——是收紧契约（要求实现真正三态）还是放宽文档（声明三态是能力上限）？当前是后者。
3. **要不要补 bconf 的熔断契约段？** 当前配置层零字段。加了才能「配了熔断就生效」。
4. **`vegas` 的 `minRTT`/`maxRTT` 要不要暴露成 Option？** 见「理由与取舍」——倾向不暴露，但「恢复必须经接口可达」可能绕不开它。
