# Bald 重试设计：三组策略正交组合，零依赖独立 module

> Author(s): bald 团队
>
> Last updated: 2026-09-17
>
> Discussion at: 源码 `retry/{go.mod,retry.go,retry_test.go}`；对照 `broker/kafka/subscriber.go`、`registry/etcd/registry.go`、`bconfig/apollo/apollo.go`
>
> Status: Accepted（已落地、零依赖；但**尚未接入任何调用点、尚未登记**，见「实现与过渡」）

## 摘要

`retry` 是 bald 的第一个统一重试契约包。它把「重试几次、每次等多久、等待要不要抖动、哪些错误才值得重试」拆成三组**正交且可替换**的策略——`Backoff`、`Jitter`、`Classifier`——由一个无状态配置对象 `Retrier` 组装，入口只有一个方法 `Do(ctx, fn)`。

我们做了三个承诺：

1. **零依赖**。`retry/go.mod` 只有 `module` 与 `go` 两行，与 `health`、`registry`、`ratelimit` 同级——它是零件，不是框架。
2. **传输无关**。它只认 `func(ctx) error`，不认 HTTP、gRPC、Kafka、etcd。任何 I/O 都能包进来。
3. **不吞错误语义**。终止时用 `errors.Join(哨兵, lastErr)`，调用方的 `errors.Is` 与我们的 `ErrMaxAttempts` **同时**成立。

需要先说清现状：这个包是 `da4d533`（六模块批量移植）搬进来的，**全仓零 import**——没有任何一处代码在用它。仓内现有的重试行为，仍然散在三个后端里各写各的。

## 背景与动机

### 仓内有三份互不相识的退避实现，而且没有一份能被复用

`broker/kafka` 的消费者自己写了一个退避函数：

```go
// broker/kafka/subscriber.go:311
// 简单的指数退避（带抖动），attempt 从 0 开始
func backoffSleep(attempt int, min, max time.Duration) {
	if min <= 0 {
		min = 50 * time.Millisecond
	}
	...
	d := min << uint(attempt)          // 2^attempt
	if d > max {
		d = max
	}
	// 添加抖动，范围 [d/2, d)
	jitter := time.Duration(randSrc.Int63n(int64(d/2 + 1)))
	sleepDur := d/2 + jitter
	time.Sleep(sleepDur)
}
```

`registry/etcd` 的 KeepAlive 断链重注册是另一套——它先把 2^n 秒攒进切片，再随机挑一个：

```go
// registry/etcd/registry.go:284
retreat = append(retreat, 1<<retryCnt)
time.Sleep(time.Duration(retreat[rnd.Intn(len(retreat))]) * time.Second)
```

`bconfig/apollo` 的「空文档等待同步」又是第三套——固定间隔，且把「空文档」也算作失败：

```go
// bconfig/apollo/apollo.go:142
func (c *Config) loadWithSyncWait(ctx context.Context, fetch func() ([]byte, error)) ([]byte, error) {
	retries := c.opts.syncWaitRetries
	...
	for attempt := 0; attempt <= retries; attempt++ {
		data, err := fetch()
		if err == nil && hasContent(data) {
			return data, nil
		}
		...
		case <-time.After(c.opts.syncWaitInterval):
	}
```

三份实现，三种语义：kafka 是「`min<<attempt` 加半抖」，etcd 是「预计算 2^n 秒随机取一个」，apollo 是「固定间隔」。它们各自的边界处理也不同——apollo 支持「负数 = 禁用等待」，kafka 支持「`max < min` 时把 max 拉回 min」，etcd 干脆没有上限。**没有任何一份能被第四处复用**，因为三份的函数签名、参数含义、终止条件全不一样。

### 契约层想表达退避，但无处可写

`bconf` 里与重试相关的字段只有两个标量：

| 位置 | message | 字段 |
|---|---|---|
| `bconf/proto/bootstrap/v1/broker.proto:107` | `Broker.Rocketmq` | `int32 retry_count` |
| `bconf/proto/bootstrap/v1/registry.proto:50` | `Registry.Etcd` | `int32 max_retry` |

**没有任何一个字段能表达退避曲线**——初始值、倍数、上限、抖动策略，一个都写不出来。于是每个后端只能把曲线硬编码在自己代码里，配置文件能调的永远只有「几次」。

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

**边界**：`Retrier` 配置在 `New` 之后不再变化，可以跨 goroutine 共享——包括注入了 `WithRNG` 的场景（对注入 `*rand.Rand` 的访问已由互斥量串行化，见「兼容性」第 3 条）。

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

**边界（口径不一致，如实说明）**：elapsed 用 `r.now()` 计算（可注入），但睡眠是真实的 `time.After(wait)`。注入假时钟做测试时，这两个口径对不上——见「兼容性」第 4 条。另一处口径：预算检查发生在每次尝试**之后**的退避计算处，循环开头只查 ctx——睡眠恰好用尽预算时，下一次 `fn` 仍会启动（其耗时超出预算），随后才在退避处返回 `ErrTimeout`。即「睡眠永不超预算，尝试可能压线多跑一次」。

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

`github.com/cenkalti/backoff/v4 v4.3.0` 以 `// indirect` 出现在根 `go.mod:96`。它有现成的指数退避、`PermanentError`、`RetryAfter` 支持。我们没用，三个原因：

1. **它是框架，我们要零件**。`backoff.Retry(op, b)` 把「退避策略」和「执行循环」绑在一起，`op` 的返回值语义被它定义。而我们的调用点需要自己控制循环里的其他事——检查熔断、等令牌、判断 ctx。
2. **引入即失去零依赖**。`retry` 的定位是「谁都拖得动」的零件；一旦 require 了第三方，它就和 `health`、`registry` 的零依赖惯例不一致了。
3. **`maxTotalWait` 与 ctx 感知的睡眠是我们要的第一等公民**，在 `backoff/v4` 里得绕。

**代价我们承认**：放弃了它成熟的 `PermanentError` 与 HTTP `Retry-After` 语义，也放弃了社区验证过的边界处理。将来若要支持 `Retry-After`，得自己加扩展点（见「开放问题」）。

### 我们没选 kratos `middleware/retry`

`kratos/v3` 在本仓只是 `// indirect`（`go.mod:111`），唯一用途是配置源桥接；`middleware/retry` 全仓零引用。选它会把重试和 HTTP/gRPC 传输绑死——而最需要重试的三处（kafka、etcd、apollo）**都不是 kratos 的传输层**。

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

## 兼容性

### 纯新增，零破坏

`retry` 是新 module，全仓搜索 `kalandramo/bald/retry` **只命中它自己的 `go.mod`**。没有任何现有调用方受影响，不需要迁移，不需要改任何现有代码。

### 边界：一处刻意保留，三处已修

经 2026-09-17 评审裁定——第 1 条**刻意保留**，第 2、3、4 条修复：

1. **`WithMaxAttempts(0)` 会被静默忽略**，回落到默认 3（`TestWithMaxAttempts_LessThanOne` 固化了这个行为）。如果你以为传 0 是「不重试」，实际会重试 3 次——**要「不重试」请用 `WithMaxAttempts(1)` 或 `WithClassifier(RetryNever)`**。我们**刻意保留**它：`circuitbreaker/vegas` 的 `WithAlpha(-1)` 忽略无效输入（`TestNew_WithAlpha_InvalidIgnored`）是同一个惯例，仓内一致优先。
2. **nil 策略不再 panic（已修）**。`WithBackoff(nil)` / `WithJitter(nil)` / `WithClassifier(nil)` / `WithClock(nil)` 改为忽略 nil、保留默认值，`Do` 不会崩。回归测试 `TestNew_NilStrategiesIgnored`。
3. **注入 RNG 后可以并发共享（已修）**。注入的 `*rand.Rand` 由 `Retrier.mu` 串行化（锁加在 `random()` 内），`rngCh` 死字段已删除。回归测试 `TestDo_ConcurrentWithRNG`——**需 `-race` 才真正作数**。
4. **时钟已可注入（已修，方案 A）**。新增导出 `WithClock(func() time.Time)`，与姊妹模块 `ratelimit/tokenbucket` 的 `WithClock`（`tokenbucket.go:38`）对齐，`New` 默认 `time.Now`。**但「口径不一致」仍在，且是我们刻意留下的**：注入的时钟只影响 `maxTotalWait` 的 elapsed 计算，睡眠仍走真实 `time.After`。收益是实的——让假时钟「每读一次就前进」，预算会在第一次退避计算时立即超支，于是**零真实等待地确定性测到 `ErrTimeout`**（`TestWithClock_DeterministicTimeout`：30s 退避不真睡，耗时 0.00s）。要让睡眠也跳过，需要 `WithSleeper` 那一层抽象；目前没有第二个消费者，不做（见「理由与取舍」的 YAGNI 取向）。

### 迁移策略：不强制迁移

`retry` 先作为**新代码的默认选择**。三处旧的就地实现**保持不动**——它们的参数与 `retry` 的默认值不同（kafka 的 `min<<attempt` 语义、etcd 的无上限随机、apollo 的「空文档也算失败」），直接替换就是行为变更，必须逐个评估，不能一把梭。

## 实现与过渡

### 已落地

`retry/{go.mod,retry.go,retry_test.go}`，三个文件、零依赖。引入于 `da4d533`（六模块批量移植），`1bcebd1` 随全模块升级到 go 1.27.1。28 个测试覆盖三组策略、attempt 上限、ctx 取消、墙钟超限、零值防御与端到端。

**2026-09-17 修复（评审裁定第 2、3、4 条）**：nil 策略改为忽略并保留默认；注入 RNG 的访问由互斥量串行化、`rngCh` 死字段删除；新增导出 `WithClock`（方案 A）。三处都补了回归测试，`go vet` 与 `go test -short` 全绿（`-race` 需 cgo，本机无 gcc）。

**2026-09-17 二轮评审修复（5 项）**：`ExponentialBackoff.Delay` 把 `Factor <= 0` 归一为 2（零值安全，回归测试 `TestExponentialBackoff_ZeroFactorDefaults`）——文档与包注释示例曾三处省略 `Factor`，照抄会得到 0 间隔重试；文档同步修正并发共享的过时「除非」条款、`LinearBackoff` 源码注释公式（`Initial*(attempt+1)` → `Initial+Step*attempt`）、测试计数（19→25）与 `maxTotalWait` 预算检查点的边界披露（检查在尝试之后，睡眠用尽预算后下一次 `fn` 仍会启动）。

**2026-09-18 审查轮修复（零值防御补全 + 登记）**：审查（天权）发现 `Factor` 的零值归一**只做了一半**——同款陷阱在 `Initial` 上未处理也未披露：`ExponentialBackoff{Initial: 0}` 的 `Delay` 恒为 0（零基数 × 任意 Factor = 0），即全零间隔热重试。修复：

| 项 | 处置 | 理由 |
|---|---|---|
| `ExponentialBackoff.Initial <= 0` | 归一为 200ms（包默认），与 `Factor` 完全对称 | 零基数的指数曲线在数学上恒为 0，不是有效策略 |
| `LinearBackoff.Initial` | **刻意不归一** | `Initial=0 + Step>0` 是合法曲线（首次立即重试、随后线性增长），归一化会破坏它 |

新增 3 个回归测试（`TestExponentialBackoff_ZeroInitialDefaults`、`TestExponentialBackoff_NegativeInitialDefaults`、`TestLinearBackoff_ZeroInitialIsValid`），25 → 28 个。Taskfile 补 `retry-verify` 并挂进根 `verify`，根 README 登记。

### 登记工作（2026-09-18 已补齐）

1. ~~**`Taskfile.yml` 里零处 `retry`**。~~ **已补**：新增 `retry-verify`（`dir: retry` 下 build+vet+test），挂进根 `verify` 的 deps。嵌套 module 不在根 `go build ./...` 范围内，故必须显式列出。
2. ~~**根 `README.md` 零处 `retry`**。~~ **已补**：架构树登记该 module。
3. **`docs/devel/zh-CN/README.md` 已收录本文**（2026-09-17 随本文补入索引）。

**CI 不需要改**：`.github/workflows/ci.yml:54` 用 `find . -name go.mod` 自动发现全部 module（仅排除 `_example*`），`retry/` 已被自动纳入 build + vet + test。

### 接入顺序：先新代码，旧实现按「行为敏感度」排队

- **可以直接接**：新写的网络 I/O、外部 API 调用。
- **必须先固定现有参数再接**：`registry/etcd` 的 KeepAlive 重注册（2^n 秒 + 随机、无上限）与 `broker/kafka` 的 fetch 退避（`min<<attempt` + 半抖）。这两处是行为敏感区，迁移前要把现有参数逐一对齐到 `retry` 的配置上，否则迁移即行为变更。
- **不在本设计范围内**：让 `bconf` 契约表达退避曲线。这需要新增 proto 字段，是一次独立的契约变更（当前只有 `retry_count` / `max_retry` 两个标量）。

### 验证方式

```bash
cd retry && go test -short ./...     # 28 个测试
cd retry && go test -race ./...      # 并发边界（需 cgo；本机无 gcc，交给 CI 或装有 gcc 的机器）
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

1. **时钟注入：已裁定方案 A 并落地（2026-09-17）**。导出 `WithClock(func() time.Time)`，`New` 默认 `time.Now`；只影响 elapsed 计算、睡眠仍走真实计时器（边界见「兼容性」第 4 条）。未采选项留档：**B**（再加一层 `WithSleeper`，让睡眠也可跳过）等真有第二个消费者再议；**C**（删掉 `timer` 字段）已否决——那会让 `ErrTimeout` 分支只能靠真实等待来测。
2. 要不要支持 HTTP `Retry-After`（429/503）？这需要一个「让分类器返回建议等待时长」的扩展点，**会动 `Classifier` 的签名**，属于破坏性变更，需要单独决策。
3. 默认抖动是否从 `NoJitter` 改为 `FullJitter`？这是行为变更，影响所有未显式配置的调用方，需要单独决策。
