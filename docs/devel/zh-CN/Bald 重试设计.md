# Bald 重试设计：三组策略正交组合，零依赖独立 module

> Author(s): bald 团队
>
> Discussion at: 源码 `retry/{go.mod,retry.go,retry_test.go}`；对照 `broker/kafka/subscriber.go`、`registry/etcd/registry.go`、`bconfig/apollo/apollo.go`
>
> Status: Accepted（已落地、零依赖；Taskfile/README 已登记；退避原语已接入 `broker/kafka` 与 `registry/etcd`，契约侧新增 `bootstrap.v1.Backoff` 并挂上 `registry.etcd.backoff`；`Retrier.Do` 尚无调用点，见「实现与验证」）

## 摘要

`retry` 是 bald 的第一个统一重试契约包。它把「重试几次、每次等多久、等待要不要抖动、哪些错误才值得重试」拆成三组**正交且可替换**的策略——`Backoff`、`Jitter`、`Classifier`——由一个无状态配置对象 `Retrier` 组装，入口只有一个方法 `Do(ctx, fn)`。

我们做了三个承诺：

1. **零依赖**。`retry/go.mod` 只有 `module` 与 `go` 两行，与 `health`、`registry`、`ratelimit` 同级——它是零件，不是框架。
2. **传输无关**。它只认 `func(ctx) error`，不认 HTTP、gRPC、Kafka、etcd。任何 I/O 都能包进来。
3. **不吞错误语义**。终止时用 `errors.Join(哨兵, lastErr)`，调用方的 `errors.Is` 与我们的 `ErrMaxAttempts` **同时**成立。

现状分两层说：`retry` 的**退避原语已被两处消费**——`broker/kafka` 的订阅 fetch 退避与 `registry/etcd` 的断链重注册退避，都换成这里的 `ExponentialBackoff` + `Jitter`，后者还把曲线与抖动的形状开成了可覆盖的选项（`WithRetreat`），并由契约段 `registry.etcd.backoff` 驱动；而**重试循环 `Retrier.Do` 在仓内仍无调用点**，`bconfig/apollo` 的空文档等待也仍是自己那一套固定间隔。判据：`grep -rl '"github.com/kalandramo/bald/retry"' --include='*.go' .` 命中 5 个文件——`broker/kafka/subscriber.go`、`registry/etcd/{options,registry}.go`、`registry/etcd/contract/contract.go` 与 `registry/etcd/retreat_policy_test.go`。

## 背景与动机

### 设计时：仓内有三份互不相识的退避实现，没有一份能被复用

写这份设计时，同一个「等一会儿再试」在三处是三套写法：

- `broker/kafka` 的消费者：`d = min << attempt` 截到 `max`，再叠一个 `[d/2, d]` 的半抖动；自带两条兜底——`min <= 0` 用 50ms、`max < min` 把 `max` 抬到 `min`。
- `registry/etcd` 的 KeepAlive 断链重注册：把 `1, 2, 4, …, 2^n` 秒攒进一个切片，再等概率挑一档——没有上限，也没有曲线，只有档位集合。
- `bconfig/apollo` 的「空文档等待同步」：固定间隔轮询，且把「空文档」也算作失败，另支持「负数 = 禁用等待」。

三种语义、三种边界，签名和终止条件全不一样，**没有任何一份能被第四处复用**。

今天 kafka 与 etcd 两处已经归一到 `retry` 的 `ExponentialBackoff` + `Jitter`（参数对齐见「实现与验证」），apollo 仍在原地——它撞的不是曲线而是 `Retrier.Do` 的签名（谓词是内容而非 error、「耗尽仍为空」是合法结果、payload 要靠闭包带出），刻意不接的理由记在风险评审 §2「retry 半」。

### 契约层想表达退避，但无处可写

`bconf` 里与重试相关的字段只有两个标量：

| 位置 | message | 字段 |
|---|---|---|
| `bconf/proto/bootstrap/v1/broker.proto` | `Broker.Rocketmq` | `int32 retry_count` |
| `bconf/proto/bootstrap/v1/registry.proto` | `Registry.Etcd` | `int32 max_retry` |

**没有任何一个字段能表达退避曲线**——初始值、倍数、上限、抖动策略，一个都写不出来。于是每个后端只能把曲线硬编码在自己代码里，配置文件能调的永远只有「几次」。

今天这条已经闭合：`bconf/proto/bootstrap/v1/common.proto` 有了一条顶层 `Backoff` 消息，第一个表面是 `Registry.Etcd.backoff`，桥在 `registry/etcd/contract`。口径、kafka 为什么暂不挂、以及翻译层的归属与升格条件见「实现与验证 → 契约曲线」。

### 一句话定性

**退避逻辑散落在各后端、契约层无法表达、新增一个后端就多抄一份。**

## 设计

### 最小可用形态：一个对象，一个方法

```go
r := retry.New(
	retry.WithMaxAttempts(5),
	retry.WithBackoff(retry.ExponentialBackoff{
		Initial: 100 * time.Millisecond,
		Factor:  2,
		Max:     5 * time.Second,
	}),
)

err := r.Do(ctx, func(ctx context.Context) error {
	return doSomething(ctx)
})
```

`New` 返回 `*Retrier`，所有配置字段私有，只能通过 `Option` 设置。`Do` 是唯一入口，签名 `func(ctx context.Context, fn func(ctx context.Context) error) error`。

**为什么 `fn` 要收一个 ctx**：让取消能继续往下传。调用方通常要把 ctx 交给 `http.NewRequestWithContext`、`db.QueryContext`——如果 `fn` 拿不到 ctx，重试虽然能被取消，但**正在执行的那次请求取消不了**，只能等它自己超时。

**边界**：`Retrier` 配置在 `New` 之后不再变化，可以跨 goroutine 共享——包括注入了 `WithRNG` 的场景（对注入 `*rand.Rand` 的访问已由互斥量串行化，见「边界与约束」§3）。

### 三组策略各自独立，互不知道对方存在

```go
type Backoff interface {
	Delay(attempt int) time.Duration
}

type Jitter func(delay time.Duration, rng func() float64) time.Duration

type Classifier func(err error) bool
```

三者由 `Do` 在运行期串起来，**彼此零耦合**：换抖动策略不需要动退避曲线，换分类器不需要动等待时长。

#### Backoff：三种曲线，`attempt` 从 0 开始

| 类型 | 语义 | 公式 |
|---|---|---|
| `ExponentialBackoff{Initial, Factor, Max}` | 指数增长（默认） | `Initial * Factor^attempt`，封顶 `Max` |
| `LinearBackoff{Initial, Step, Max}` | 线性增长 | `Initial + Step*attempt`，封顶 `Max` |
| `FixedBackoff(d)` | 恒定间隔 | 永远是 `d` |

`Delay(attempt)` 的 `attempt` 是 **0-based 的重试序号**：`attempt=0` 表示「第一次失败之后、第二次尝试之前」要等多久。所以 `ExponentialBackoff{Initial: 100ms, Factor: 2}.Delay(0) == 100ms`，`Delay(1) == 200ms`。

这个「差一位」是刻意的：`MaxAttempts` 数的是**尝试次数（含首次）**，`Delay` 描述的是**等待**。两者语义不同，共用一个从 1 开始的计数器反而更容易错。

`Max <= 0` 表示不封顶；`Factor <= 0` 归一为 2（零值安全——省略 `Factor` 的结构体字面量不会把曲线塌缩成 0 间隔）。

#### Jitter：把随机源作为参数传进来，而不是内部偷偷用全局 rand

```go
func NoJitter(delay time.Duration, _ func() float64) time.Duration { return delay }

// 返回 [0, delay) 之间的随机值
func FullJitter(delay time.Duration, rng func() float64) time.Duration {
	return time.Duration(float64(delay) * rng())
}

// 返回 [delay/2, delay) 之间的随机值
func EqualJitter(delay time.Duration, rng func() float64) time.Duration {
	half := delay / 2
	return half + time.Duration(float64(half)*rng())
}
```

`Jitter` 的签名里带 `rng func() float64`，是为了让 `WithRNG(rand.New(rand.NewSource(42)))` 能接管随机性——**测试可以断言精确的等待时长**，不用 sleep 完再猜。

`FullJitter` 与 `EqualJitter` 出自 AWS 那篇 *Exponential Backoff and Jitter*。区别很实在：`FullJitter` 期望值是 `delay/2`、方差最大；`EqualJitter` 保证至少等 `delay/2`，方差小一些，**牺牲一部分去相关性换取下限**。

#### Classifier：只回答「值不值得再试」，不做任何转换

```go
func RetryAny(err error) bool  { return true }
func RetryNever(err error) bool { return false }
func RetryIf(pred func(err error) bool) Classifier { return pred }

// 典型用法：只重试可恢复的错误
r := retry.New(retry.WithClassifier(retry.RetryIf(func(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, context.DeadlineExceeded)
})))
```

`Classifier` 返回 `bool` 而不是 `error`——返回 error 会诱导「分类器顺手把错误转换一下」，职责就糊了。它只回答一个是非题：**这个错误，值不值得再试一次。**

**边界**：分类器只在 `fn` 返回**非 nil** 错误后被调用，所以 `RetryAny` 忽略参数是安全的。分类器返回 `false` 时，`Do` 原样返回那个错误——**不包装**。

### 三条终止线，谁先到谁说话

`Do` 的循环里有三个独立的终止条件，这是最容易讲不清的地方，所以我们把判定顺序列全：

| 触发时机 | 返回的 error | 是否包装 |
|---|---|---|
| 进入某次尝试前 `ctx` 已取消 | `ctx.Err()` | 否 |
| 等待期间 `ctx` 取消 | `ctx.Err()` | 否 |
| `classifier` 返回 false | `lastErr` | **否**（原样） |
| 尝试次数用尽 | `errors.Join(ErrMaxAttempts, lastErr)` | 是 |
| 累计等待超过 `maxTotalWait` | `errors.Join(ErrTimeout, lastErr)` | 是 |

`maxTotalWait` 是**墙钟总预算**，覆盖「所有尝试 + 所有退避睡眠」，不是单次超时：

```go
r := retry.New(
	retry.WithMaxAttempts(100),
	retry.WithBackoff(retry.FixedBackoff(50*time.Millisecond)),
	retry.WithMaxTotalWait(100*time.Millisecond), // 只给 100ms
)
// 实际会在第 3 次左右返回 ErrTimeout，而不是跑满 100 次 * 50ms = 5s
```

预算还剩多少，就最多睡多少——`wait = min(wait, remaining)`，所以**最后一次等待不会被截断成「睡过头」**，而是刚好用尽预算后返回 `ErrTimeout`。

**边界（口径不一致，如实说明）**：elapsed 用 `r.now()` 计算（可注入），但睡眠是真实的 `time.After(wait)`。注入假时钟做测试时，这两个口径对不上——见「边界与约束」§4。另一处口径：预算检查发生在每次尝试**之后**的退避计算处，循环开头只查 ctx——睡眠恰好用尽预算时，下一次 `fn` 仍会启动（其耗时超出预算），随后才在退避处返回 `ErrTimeout`。即「睡眠永不超预算，尝试可能压线多跑一次」。

### 组合位置由调用方决定，`retry` 不感知熔断与限流

`retry` 刻意不认识 `circuitbreaker` 和 `ratelimit`。三者的组合顺序是一个**语义问题**，不是一个 API 问题，所以判据留给调用方：

- 重试放**熔断内层**：一次 attempt = 一次熔断统计样本。熔断器能看到真实的失败率，但重试会放大请求量（3 次重试 = 3 个样本）。
- 重试放**熔断外层**：熔断打开时 `retry` 会白等几个退避周期才拿到 `ErrCircuitOpen`，但熔断器统计的是「逻辑请求」而非「物理请求」。

下面只是顺序示意（`Allow`/`Wait` 为本仓两个姊妹包的接口形状，非可直接编译的调用）：

```go
err := r.Do(ctx, func(ctx context.Context) error {
	if !cb.Allow() {                    // 熔断：这次 attempt 还允许发吗
		return circuitbreaker.ErrCircuitOpen
	}
	if err := limiter.Wait(ctx); err != nil { // 限流：等令牌
		return err
	}
	return doSomething(ctx)
})
```

## 理由与取舍

### 我们没选 `cenkalti/backoff/v4`，尽管它已经在依赖树里

`github.com/cenkalti/backoff/v4 v4.3.0` 以 `// indirect` 出现在根 `go.mod`。它有现成的指数退避、`PermanentError`、`RetryAfter` 支持。我们没用，三个原因：

1. **它是框架，我们要零件**。`backoff.Retry(op, b)` 把「退避策略」和「执行循环」绑在一起，`op` 的返回值语义被它定义。而我们的调用点需要自己控制循环里的其他事——检查熔断、等令牌、判断 ctx。
2. **引入即失去零依赖**。`retry` 的定位是「谁都拖得动」的零件；一旦 require 了第三方，它就和 `health`、`registry` 的零依赖惯例不一致了。
3. **`maxTotalWait` 与 ctx 感知的睡眠是我们要的第一等公民**，在 `backoff/v4` 里得绕。

**代价我们承认**：放弃了它成熟的 `PermanentError` 与 HTTP `Retry-After` 语义，也放弃了社区验证过的边界处理。将来若要支持 `Retry-After`，得自己加扩展点（见「开放问题」）。

### 我们没选第三方传输中间件的重试

第三方传输中间件的重试全仓零引用。选它会把重试和 HTTP/gRPC 传输绑死——而最需要重试的三处（kafka、etcd、apollo）**都不在那些传输层里**。

### 我们没选 gRPC 的 service-config 重试

`grpc.WithDefaultServiceConfig` + `retryPolicy` 全仓零命中。它只覆盖 gRPC 一种传输，重试策略写成 JSON 字符串（静态、不可编程），且无法与 `Classifier` 这种运行期判定组合。

### 我们没把三组策略合成一个大 `Options` 结构

合成一个结构看起来更「整洁」，但会让「只想换抖动策略」的人被迫碰退避曲线的字段，也让默认值集中膨胀到一处。拆成三个接口后，**每一组都能单独替换、单独测试、单独在文档里讲清**——`retry_test.go` 里 28 个测试正好按这三组分开。

### 错误包装用 `errors.Join`，而不是自定义 `RetryError` 类型

调用方需要**同时**知道两件事：「重试耗尽了」和「最后一次到底是什么错」。自定义类型会逼调用方 `errors.As` 到我们的类型再取字段；`errors.Join(ErrMaxAttempts, lastErr)` 让两边的 `errors.Is` 都成立：

```go
if errors.Is(err, retry.ErrMaxAttempts) && errors.Is(err, syscall.ECONNRESET) {
	// 既知道是耗尽，也知道根因是连接被重置
}
```

**代价**：`errors.Join` 的 `Error()` 用换行符连接，文本是两行。直接塞进单行日志会断行。要单行就自己 `strings.ReplaceAll(err.Error(), "\n", "; ")`。我们接受这个代价，换 `errors.Is` 的干净。

### 默认是 `NoJitter`，而不是 AWS 推荐的 `FullJitter`

这一条是有争议的取舍，写清楚：**AWS 的建议针对的是「大量客户端同时重试」的惊群场景**；而作为**库的默认值**，随机化会让「为什么这次等 1.2s、下次等 0.3s」变得难以复现，也让基于计时的测试变脆。

所以我们把决定权交给调用方，默认保持确定性。**如果你在写面向公网的重试，应该显式 `WithJitter(FullJitter)`。**

### 不做泛型返回值 `Do[T]`

`Do[T any](ctx, fn func(ctx) (T, error)) (T, error)` 看起来更省事，但重试的绝大多数场景只需要 error，而泛型化会让唯一入口的签名复杂化、错误包装的语义也更难讲。要返回值就在闭包里捕获：

```go
var body []byte
err := r.Do(ctx, func(ctx context.Context) error {
	var err error
	body, err = fetch(ctx)
	return err
})
```

**承认**：这确实多两行闭包。我们换的是 API 形状的稳定。

## 边界与约束

### 1. `WithMaxAttempts(0)` 被静默忽略

传入 0 会回落到默认 3（`TestWithMaxAttempts_LessThanOne` 固化了这个行为）。如果你以为 0 是「不重试」，实际会重试 3 次——要「不重试」请用 `WithMaxAttempts(1)` 或 `WithClassifier(RetryNever)`。

这是刻意保留的惯例：`circuitbreaker/vegas` 的 `WithAlpha(-1)` 同样忽略无效输入。仓内一致优先于「参数非法即报错」。

### 2. nil 策略被忽略，不 panic

`WithBackoff(nil)` / `WithJitter(nil)` / `WithClassifier(nil)` / `WithClock(nil)` 忽略 nil、保留默认值，`Do` 不会崩（`TestNew_NilStrategiesIgnored`）。配置由可选 Option 拼装时，nil 是很自然的中性值。

### 3. 注入的 RNG 可并发共享

注入的 `*rand.Rand` 由 `Retrier.mu` 串行化（锁在 `random()` 内）。不注入时用自己的随机源（`rngCh` 死字段已清除）。`TestDo_ConcurrentWithRNG` 需 `-race` 才真正作数。

### 4. 时钟只影响墙钟预算，不影响睡眠（刻意）

`WithClock(func() time.Time)` 与姊妹模块 `ratelimit/tokenbucket` 对齐，`New` 默认 `time.Now`。但注入的时钟只影响 `maxTotalWait` 的 elapsed 计算，睡眠仍走真实 `time.After`。

收益是实的：假时钟「每读一次就前进」时，预算会在第一次退避计算时立即超支，于是**零真实等待地确定性测到 `ErrTimeout`**（`TestWithClock_DeterministicTimeout`：30s 退避不真睡，耗时 0.00s）。要让睡眠也跳过，需要 `WithSleeper` 那一层抽象；目前没有第二个消费者，不做。

### 5. 零值防御是不对称的

`ExponentialBackoff` 的 `Factor <= 0` 归一为 2、`Initial <= 0` 归一为 200ms——零基数的指数曲线恒为 0，不是有效策略。`LinearBackoff.Initial` **刻意不归一**：`Initial=0 + Step>0` 是合法曲线（首次立即重试、随后线性增长）。回归测试覆盖三个方向。

### 6. 预算检查点在尝试之后

`maxTotalWait` 的检查发生在一次尝试结束后、下一次退避之前。睡眠用尽预算后，下一次 `fn` 仍会启动一次——这是「至少尝试一次」的语义，不是漏检。

## 实现与验证

`retry` 单个 module、零依赖、三个文件。28 个测试覆盖三组策略、attempt 上限、ctx 取消、墙钟超限、零值防御、并发 RNG 与端到端。

`Taskfile.yml` 的 `retry-verify` 已挂进根 `verify`；根 `README.md` 已登记模块树；CI 用 `find . -name go.mod` 自动发现全部 module。

### 接入现状：两处退避已归一，参数逐条对齐

迁移按「行为敏感度」排队——先把现有参数钉到 `retry` 的配置上，再换实现。

| 位置 | 旧实现 | 新配置 | 行为差别 |
|---|---|---|---|
| `broker/kafka` 订阅 fetch 退避 | `d = min<<attempt` 截 `max`，再取 `[d/2, d]` 的半抖 | `ExponentialBackoff{Initial: min, Factor: 2, Max: max}` + `EqualJitter` | **在调用点上逐点等价**，曲线与抖动区间重合（`rng=1` 恰好取到 `d`，`rng∈[0,1)` 时上端点取不到）。两条 kafka 自兜底留在原处：`min<=0` → 50ms（`retry` 的零值兜底是 200ms，不改它）、`max<min` 抬到 `min`。唯一差别在纯函数边界：旧写法 `min<<attempt` 在 attempt ≥ 64 时位移回绕，被紧随的 `if d < min` 兜回 `min`——退避反转成贴地重试；新写法越过 `max` 就停在 `max`。今天循环里 `errorAttempt` 封顶在 6（最高档早截到 `max`），所以这条差异线上不可达，测试按 attempt 64/100 把它钉住 |
| `registry/etcd` 断链重注册退避 | `1,2,4,…,2^n` 秒攒进切片、等概率取一档，无上限 | `ExponentialBackoff{Initial: 1s, Factor: 2, Max: defaultRetreatMax}` + `FullJitter` | **分布变了，如实记账**：默认 `max_retry=5`（最高档 16s）未触顶，所以默认配置的上界不变。变的是三件事——① ≥9 档截到 5min。旧曲线实测（纯算术）attempt=9 → 8m32s、20 → 291h、33 → 272 年，`max_retry` 是契约字段，配置可达；≥34 起「秒 × 1e9」溢出成**负** duration，`time.After` 立刻返回，退避反转成忙轮询。② 不再等概率取档：旧写法在档位很多时仍有 `1/(n+1)` 的概率只退 1s，注册中心恢复瞬间成百上千实例一起重连正是要避免的同步风暴。③ 下限不再保 1s |
| `bconfig/apollo` 空文档等待 | 固定间隔 `syncWaitInterval`，空文档也算失败 | **不接** | 三条都撞 `Retrier.Do` 的签名：谓词是内容而非 error，`Classifier` 吃不到（要接得先造内部哨兵错误）；「重试耗尽仍为空」是合法结果（返回空文档 + nil error），而 `Do` 的终态是 `errors.Join(ErrMaxAttempts, lastErr)`；取回的 payload 要靠闭包变量带出，`Do` 的返回值里没有它的位置 |

### 契约曲线：已落地，第一个表面是 `registry.etcd.backoff`

`bconf/proto/bootstrap/v1/common.proto` 新增顶层消息 `Backoff`（形状仿已有的 `Server.TLS` / `RateLimit` / `Timeout`——被多处引用的策略段，不是每个后端各抄一份字段）：`initial` / `factor` / `step` / `max` / `curve`（`EXPONENTIAL`、`LINEAR`、`FIXED`）/ `jitter`（`NONE`、`FULL`、`EQUAL`），首个挂点是 `Registry.Etcd.backoff = 7`，由各 `<adapter>/contract` 桥接成 `retry` 的 `Backoff` + `Jitter`。

**缺省分两层**，这是这份契约里最容易写歪的地方：

| 层 | 口径 |
|---|---|
| 段缺席（`optional`） | 整条曲线沿用后端默认——etcd 即 1s 起翻倍、截到 `defaultRetreatMax`、叠 `FullJitter`。「不配置即行为不变」的实现路径是 `retreat(nil)` 返回**两个 nil**，不是零值消息 |
| 段存在 | `initial` 必填且 `>0`；除 `FIXED` 外 `max` 必填且 `>0`；`factor` 未写（0）由 `retry` 自己按 2 归一；`jitter` 未写（`UNSPECIFIED`）落回 nil，即**只换曲线、不换抖动** |

刻意**不做**「逐字段继承后端默认」。那条口径要求契约层知道每个后端的默认数字——同一个数字同时住在 `bconf` 和后端实现里，两边各自演进必然漂移；代价换成「写了就要写全这条曲线的形状」，可接受。

两个形状因此**不可表达**，直接判配置错而不是静默降级：零原点（`initial<=0`，退避就是忙轮询）与无上界指数（缺 `max`，高位次乘出负 duration，退避反转成忙轮询）。两条反向失效同一个症状，正是 `defaultRetreatMax` 那条注释记过的坑，所以判错文案点名键（`initial` / `max` / `step`），不让运维只看到一层 wrapper。

**kafka 为什么没在这一批挂上**：`broker/kafka` 的退避旋钮是 `SubscribeOption` 里的 `WithReadBackoffMin/Max`，实测**零调用者**，而且 `broker/kafka/contract` 的 `Provider` 不被仓内代码调用。把 `Backoff` 挂到 `broker.kafka` 段，等于在 Provider 层造一个「写了没人读」的字段——那属于风险评审 §2 的 reference-only 口径，不进契约批次。

**翻译层的归属**：本轮只有 1 个消费者，所以 `retreat()` 放在 `registry/etcd/contract`，没有先抽共享包。第二个表面接入时再升格，升格时边方向定为 `retry ← 桥`，不给 `bconf` 加依赖：`bconf → retry` 是侵入性最强的一条边（`bconf` 是只依赖 `pflag` + `protobuf` 的图底，加一条边波及全仓 module 与 `bald release sync` 的涟漪）；`retry → bconf` 则便宜得多，因为能挂契约段的消费者（`registry/etcd`、`broker/kafka`）本来就 require `bconf`。另一条相关约束：`registry/etcd` 根包保持**零契约依赖**，所以 `retreatPolicy` 只能持 `retry.Backoff` / `retry.Jitter`，不能持 bconf 枚举。

**契约变更的涟漪**（本批实测）：`registry/etcd` 与 `broker/kafka` 因为新 require `retry`，把反向依赖推给了 `transport/kafka` 与 `_example/bald`——两处 `go.mod` 缺 `// indirect` 行、`go.sum` 缺条目，CI 的 `test`/`example` 作业直接判红。修法是逐模块 `go mod tidy`，判据本身不动；改完 `find . -name go.mod` 全量 `go mod tidy -diff` 复扫归零。

**证据链（四层，各测一件事）**：

| 层 | 判据 | 钉住的东西 |
|---|---|---|
| `bconf` 解码 | `TestUnmarshalNestedBackoffSection`、`TestUnmarshalAbsentBackoffStaysNil`、`TestUnmarshalDurationSpellings` | 契约段**写得进**：三层嵌套 + 嵌套里的 `Duration` WKT + 两个枚举名；缺席必须是 nil；`10s`/`1.5s`/`1500ms`/`1m30s` 四种写法落进同一个值 |
| `registry/etcd/contract` 桥 | `backoff_test.go` 7 条（缺席、三曲线、抖动映射、非法形状表、Provider fail-fast） | 段**翻得对**，且坏段在建连之前拦下 |
| `registry/etcd` 策略 | `retreat_test.go` 4 条 + `retreat_policy_test.go` 2 条 + `heartbeat_retreat_test.go` 2 条 | 默认曲线与 `retreatDelay` 逐点相同（防两条路长成两样）、`WithRetreat` 的两半各自独立覆盖、**每次失败之间真的等了**（不等就落回 §「契约曲线」末尾那条缺陷） |
| `registry/etcd/contract` 运行期 | `mapping_runtime_test.go` 3 条 | 段**投递得到**：契约值出现在打到后端的那次调用里——`Put` 的 key、`Grant` 的 TTL、`Authenticate` 的 name/password、断链后的尝试**次数**与尝试之间的**间隔** |

这四层落在三条验收腿里（`registry/etcd` 根包、`registry/etcd/contract`、`bconf`，见 `scripts/acceptance-gate.sh` 的 `scope=ci` 表——「桥」与「运行期」共用 `contract` 那一条，正则把两批用例一起点名），由 `acceptance` 作业逐条断真 `--- PASS`，所以它们不是「本机跑过就算过」。副作用也如实记：`registry/etcd` 根包进了表之后，docscheck 的归属闸门（`TestAcceptanceTableCoversShortSkippedTests`）把这个包里**既有**的四条 `-short` 跳过用例照了出来——它们一直每天在 `integration` 作业里跑，但没被断言。修法是把四条名字加进那条作业的 `EXPECTED`（etcd 那组三条变七条），既不是撤腿也不是写豁免；反向实测（地址指向死端口）套件 `exit=0` 而 7 条 SKIP，断言步 `exit=1` 逐条点名。细节记在风险评审 §7.1 的「etcd 四条 `-short` 判据」补条与 §7.14。

顺带修掉一个**既有产品缺陷**（不止影响新字段）：`bconf` 的 `coerceDuration` 字符串分支原先「只校验合法、不重排格式」，于是 `1500ms`、`5m` 这类合法的 Go duration 写法会被 protojson 拒掉（`invalid google.protobuf.Duration value "1500ms"`）。所有走契约的 `Duration` 字段都受影响，`app.stop_timeout` 是同一条路径上的先例。修法是解析后再 `formatDuration` 输出十进制秒串，`TestUnmarshalDurationSpellings` 把四种写法钉住。

**装配那一格已闭合（2026-11-05），并且它是靠换观测方向闭合的**：`Provider` 里把契约段接进 options 的那七行 `append` 原先**没有判据**——变异验活确认删掉 `opts = append(opts, etcd.WithRetreat(b, j))` 后 `registry/etcd/contract` 全部测试仍绿（一条**存活变异**）。当时的判断是「要么需要真实 etcd，要么需要给 `Registry` 加只读 accessor 污染公开 API」，两个都不接受，于是把它挂成后续批次。实际收口既没等真 etcd 也没加 accessor：`mapping_runtime_test.go` 在测试进程里起一个假 etcd gRPC 服务（只实现 `LeaseGrant`/`Put`/`LeaseKeepAlive`/`Authenticate`，其余由 `Unimplemented*Server` 兜住），再让 `LeaseKeepAlive` 响应 `TTL=0` —— clientv3 见 TTL≤0 就关闭用户续约通道（`client/v3/lease.go` 的 handleResponse），于是**断链成了确定事件**，而重注册尝试的次数与到达时刻都由服务端记录。七行字段各有落点：`endpoints`（不接则 `etcd.New` 直接报错）、`prefix`（`Put` 的 key）、`ttl`（`Grant` 的 TTL）、`username`+`password`（`Authenticate` 的请求体）、`max_retry`（断链后恰好几次 `Grant`）、`backoff`（相邻两次 `Grant` 的间隔）。十条变异逐条判红，明细见风险评审 §7.14。

两条口径值得单独记住，因为它们都是**判据设计**的错，不是阈值的错：

- **判「次数」要求把所有可能的尝试都装进观测窗口。** 第一版把「断链后恰好 3 次」写在退避那条腿里，而那条腿是 FIXED 2s——默认 maxRetry=5 的第 4、5 次排在 6s 与 8s，落在 1.2s 收尾窗口之外，于是「契约 3 次」和「默认 5 次」在窗口里同形，`WithMaxRetry` 那行的变异**判绿**。换成 fast 退避（FIXED 50ms）的独立用例后，5 次全在 ~200ms 内抵达，窗口 1.5s，同一条变异才交出「观测到 5 次, want 恰好 2」。
- **`username`/`password` 只能合并判。** clientv3 的 `getToken` 在两者任一为空时直接返回，不发 `Authenticate`（`client/v3/client.go`），所以任一行缺失的表征都是「零条」，分不出缺哪一行——错误文案照这个事实写，不假装能定位。

还有一条是**行为面**的，写这层判据时才露出来：退避 spy 在一整轮断链里交出 `attempts=[]`——`heartBeat` 的三支失败里只有「重注册成功但 `KeepAlive` 建流失败」那支等退避，另外两支直接进下一次；而实测对不可达端点一次尝试要 18.994s 才失败（clientv3 不快速失败），所以「被 3s 上限掐掉」才是常态路径。也就是说契约字段写得再对，管的也是一条几乎走不到的分支。已修：每次失败之间都等，末次尝试之后不再等（后面没有下一次了）。两处判据与变异记风险评审 §7.17。

### 验证

```bash
cd retry && go test -short ./...     # 28 个测试
cd retry && go test -race ./...      # 并发边界（需 cgo）

# 消费侧的退避判据（注入 rng，确定性断边界，不碰墙钟）
cd broker/kafka && go test -count=1 -run 'TestBackoffDuration' .
cd registry/etcd && go test -count=1 -run 'TestRetreatDelay|TestRetreatPolicy' .

# 下面两把要碰墙钟（各约 2s / 6s），因为判的是「真的等了」与「字段抵达了那次调用」
cd registry/etcd && go test -count=1 -run 'TestHeartBeatRetreat' .
cd registry/etcd && go test -count=1 -run 'TestRetreat|TestProvider_BackoffSection|TestProvider_ContractSections|TestProvider_MaxRetrySection|TestProvider_AuthSection' ./contract

# 契约段的解码判据
cd bconf && go test -count=1 -run 'TestUnmarshal' .

# 上面这几条腿在 CI 里的执行证据：判据表在 scripts/acceptance-gate.sh，
# 每条先 `go test -list` 取真命中再逐条断 `--- PASS`（命中 0 个即红，Skip 也算红）
bash scripts/acceptance-gate.sh ci
```

## 附录

### 完整 API

| 类别 | 导出符号 | 默认值 |
|---|---|---|
| 构造 | `New(opts ...Option) *Retrier` | — |
| 配置 | `WithMaxAttempts(int)` | `3` |
| | `WithBackoff(Backoff)` | `ExponentialBackoff{200ms, 2, 10s}` |
| | `WithJitter(Jitter)` | `NoJitter` |
| | `WithClassifier(Classifier)` | `RetryAny` |
| | `WithMaxTotalWait(time.Duration)` | `0`（不限） |
| | `WithRNG(*rand.Rand)` | 全局 `rand.Float64` |
| | `WithClock(func() time.Time)` | `time.Now`（只影响 elapsed 计算，不改变真实睡眠） |
| 执行 | `(*Retrier).Do(ctx, fn) error` | — |
| 哨兵 | `ErrMaxAttempts`、`ErrTimeout` | — |
| 退避 | `Backoff`、`ExponentialBackoff`、`LinearBackoff`、`FixedBackoff` | — |
| 抖动 | `Jitter`、`NoJitter`、`FullJitter`、`EqualJitter` | — |
| 分类 | `Classifier`、`RetryAny`、`RetryNever`、`RetryIf` | — |

### FAQ

**为什么类型叫 `Retrier` 而不是 `Retry`？** 包名已经是 `retry`，`retry.Retry` 是 Go 里典型的 stutter，`go vet` 也不喜欢。

**为什么 `Delay(attempt)` 从 0 开始？** 见「设计」——它数的是「第几次重试前的等待」，不是「第几次尝试」。

**为什么哨兵值不带次数和耗时？** 保持它们能被 `errors.Is` 直接比较。要细节就取 `errors.Join` 里的 `lastErr`，或自己数 attempt。

**`RetryAny` 对 nil error 也返回 true，不会有问题吗？** 不会。`Do` 只在 `fn` 返回非 nil 错误后才调用分类器。

**和 `cenkalti/backoff` 到底差在哪？** 见「理由与取舍」第一条：它是框架，我们是零件；我们要零依赖。

### 开放问题

1. **要不要再加一层 `WithSleeper`，让睡眠也可跳过？** 当前 `WithClock` 只影响 elapsed 计算（见「边界与约束」§4）。加 `WithSleeper` 能让超时测试完全脱离真实计时器，但目前没有第二个消费者，暂不做。
2. 要不要支持 HTTP `Retry-After`（429/503）？这需要一个「让分类器返回建议等待时长」的扩展点，**会动 `Classifier` 的签名**，属于破坏性变更，需要单独决策。
3. 默认抖动是否从 `NoJitter` 改为 `FullJitter`？这是行为变更，影响所有未显式配置的调用方，需要单独决策。
