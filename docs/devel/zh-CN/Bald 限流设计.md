# Bald 限流设计：契约只留三个方法，三种算法各自独立 module

> Author(s): bald 团队
>
> Last updated: 2026-09-17
>
> Discussion at: 源码 `ratelimit/{go.mod,ratelimit.go,ratelimit_test.go}` 与三个实现 `ratelimit/{tokenbucket,bbr,sentinel}/`；对照 `bconf/proto/bootstrap/v1/server.proto` 的 `Middleware.RateLimit`、`pkg/middleware/bundle/bundle.go`
>
> Status: Accepted（契约与三实现已落地；**全仓零调用点**。并发型限流器的释放面由 `InflightLimiter` 表达，见「设计」与「契约边界」）

## 摘要

`ratelimit` 是 bald 的限流契约包。它只定义一件事：**一个请求该不该现在放行**。为此它留了两个接口：基础契约 `Limiter` 三个方法——`Allow()`（立刻回答）、`Wait(ctx)`（等到放行或取消）、`Close()`——外加一个哨兵 `ErrLimited`；跟踪并发的实现再叠加一个 `InflightLimiter`，用 `Done(rtt)` 把放行与释放配成对。

三个算法各自住在独立 module 里，通过同一个接口挂上去：

| 实现 | 定位 | 依赖 |
|---|---|---|
| `ratelimit/tokenbucket` | 固定速率令牌桶，**默认选择** | 零第三方依赖 |
| `ratelimit/bbr` | 按观测 RTT 自适应估算放行量 | 零第三方依赖 |
| `ratelimit/sentinel` | Alibaba Sentinel 桥接，规则交给 Sentinel 自己管 | `sentinel-golang v1.0.4` |

我们做了三个承诺：

1. **契约极薄**。`ratelimit` 根 module 的 `go.mod` 只有 `module` 与 `go` 两行，与 `health`、`retry`、`registry` 同级——它是零件，不是框架。业务代码只 import 契约，不 import 任何算法。
2. **算法可插拔且互不感知**。换算法只改构造那一行，调用点不动；`tokenbucket` 不知道 `bbr` 存在，`bbr` 也不 import 任何限流库。
3. **「需要释放」是类型能表达的事**。并发型限流器不会假装成普通 `Limiter`——它多实现一个 `InflightLimiter`，调用方一次类型断言就能发现「这个限流器必须成对调用」。基础 `Limiter` 上不留一个多数实现为空的 `Done()`。

需要先说清现状：**全仓零 import**——`bconf` 契约里有 `server.http.middleware.rate_limit` 三个字段（`rate`/`burst`/`wait`），但没有任何 Go 代码去读它；`pkg/middleware/bundle` 的 gin 链里也没有限流层。

## 背景与动机

### 契约层写了限流字段，但没有任何代码去读它

`bconf/proto/bootstrap/v1/server.proto` 把限流声明成了一个 HTTP 中间件：

```proto
// RateLimit 限流中间件。
message RateLimit {
  // 每秒允许的请求数。
  double rate = 1;
  // 突发容量。
  int32 burst = 2;
  // 是否使用等待模式（平滑限流）。
  bool wait = 3;
}
```

字段名与 `tokenbucket.New(rate, burst)` 的参数一一对应，`wait` 显然是为 `Wait(ctx)` 准备的。但全仓搜 `GetRateLimit` 只命中生成的 `server.pb.go`——**契约写完了，装配没写**。于是「配置文件里写 `rate_limit: {rate: 100}` 就能限流」这件事，今天做不到。

### 三方法接口对并发型限流器不够用

`Limiter` 只有三个方法（`ratelimit.go`）：

```go
type Limiter interface {
	Allow() (ok bool, err error)
	Wait(ctx context.Context) error
	Close() error
}
```

对令牌桶，这是完整的一对：拿令牌、等令牌、关掉。

对 `bbr` 和 `sentinel` 的并发型规则，它**少了一半**——限流器需要知道请求什么时候结束。`bbr` 把释放能力放在具体类型上（`bbr.go` 的 `Done(rtt time.Duration)`），`sentinel` 放在具体类型上（`sentinel.go` 的 `ReleaseEntry(e)`）。若只把它们投影成 `Limiter`，`bbr.Allow()` 放行时加上的 `inflight`（`bbr.go`）永远减不回去：

```go
var l ratelimit.Limiter = bbr.New()   // 接口里没有 Done
l.Allow()  // true，inflight: 0 → 1
l.Allow()  // false —— 冷启动时 maxInflight 被夹到 1，inflight(1) >= 1
l.Allow()  // false
// 之后每一次都是 false，inflight 永远停在 1
```

`bbr.New()` 不带参数时 `estimateMaxQPSLocked` 因为窗口为空而返回 `minQPS = 1`，`maxInflight = int64(1 * 0.8) = 0`，被 `if maxInflight < 1` 夹成 1。所以**只经 `Limiter` 使用时，BBR 只放行第一个请求，之后永久拒绝**。`Wait(ctx)` 同理：它内部轮询 `Allow()`（`bbr.go`），拿到的那次放行同样把 `inflight` 加上去。

`sentinel` 是另一种形状的错位。它的并发型规则需要 entry 句柄才能释放（`AllowEntry`/`ReleaseEntry(e)`），而 `Allow()` 把 entry 直接丢掉（`sentinel.go`）。因此并发型规则的计数器只能走句柄式接口，不能投影成 `Done(rtt)`。

契约的答案是**把「需要释放」写进类型**：新增 `InflightLimiter` 子接口，`bbr` 实现它；`sentinel` 不是 `InflightLimiter`——它的释放要带 entry 句柄，而 `Done(rtt)` 不带句柄，硬塞进去只能改基础契约的 `Allow` 签名。这条差异写在类型上，调用方一次断言就能知道。

### 一句话定性

**契约层已经声明了限流，但没有装配；契约形状对「固定速率」够用，对「并发数」需要额外一个接口；三种算法的构造签名与非法配置处理各不相同。**

## 设计

### 最小形态：两个接口、一个哨兵

```go
var ErrLimited = errors.New("ratelimit: rate limit exceeded")

type Limiter interface {
	Allow() (ok bool, err error)
	Wait(ctx context.Context) error
	Close() error
}
```

实现必须并发安全——这是写在接口注释里的硬要求（`ratelimit.go`），也是三个实现唯一共同遵守的纪律。

**为什么 `Allow` 返回 `(bool, error)` 而不是 `bool`**：拒绝有两种，将来可能要区分。「超限」用 `ErrLimited`；「限流器已关闭」现在也复用 `ErrLimited`（`tokenbucket.go`、`bbr.go`），这是已知的语义混用，见「契约边界」第 1 条。签名留了 error 位，才有修正空间。

**为什么 `Wait` 收 `ctx`**：等待必须是可取消的。否则一次 `Wait` 能把 goroutine 挂到进程结束——`tokenbucket` 在 0.01 req/s 下要等 100 秒（`tokenbucket_test.go` 就是拿这个测取消的）。

### 并发型限流器多实现一个接口，而不是往 `Limiter` 里加方法

```go
type InflightLimiter interface {
	Limiter
	Done(rtt time.Duration)
}
```

`Done` 是「放行与释放配对」的那一半。`Allow` 成功即占用，`Done` 归还；漏一次 `Done` 就永久少一格容量。**`Wait` 同样占用**：`bbr.Wait` 内部轮询 `Allow`（`bbr.go`），成功返回时已占住一个 inflight 槽——对 `InflightLimiter` 而言，成功的 `Wait` 也必须配一次 `Done`（契约注释已写明）。

调用方通过一次类型断言发现它，而不是靠读文档：

```go
if il, ok := limiter.(ratelimit.InflightLimiter); ok {
	start := time.Now()
	if ok, err := il.Allow(); err != nil || !ok {
		// 超限
		return
	}
	defer func() { il.Done(time.Since(start)) }()
}
```

`defer` 必须只挂在放行成功的分支上——`bbr.Done` 无条件 `inflight--`，拒绝路径多调一次 `Done` 就让计数变负，并发限制随之失效。

**为什么 `Done` 带 `rtt` 参数**：这是 `bbr` 唯一需要的额外输入——它的滑动窗口要靠每次请求的耗时来估算可持续放行量。不带参数的话，`bbr` 就得另起一个 `DoneWithRTT(rtt)`，接口本身反而不能表达「bbr 的正确用法」。`rtt` 对不需要它的实现是噪音，所以契约明确写了「不需要就忽略，不知道就传 0」。

**为什么 `Done` 不带句柄**：带句柄的释放（`sentinel` 的 `ReleaseEntry(e)`）意味着 `Allow` 必须返回句柄，而那会改掉 `Limiter.Allow` 的签名——基础契约就得为一种实现让路。所以 `sentinel` **不是** `InflightLimiter`，它的并发型规则继续走 `AllowEntry`/`ReleaseEntry`。这条边界写在类型上，调用方一次断言就能知道。

**为什么 `tokenbucket` 不是 `InflightLimiter`**：令牌桶没有「占用」概念，令牌一旦消耗就结束了。它不需要 `Done`，也不该被迫实现一个空方法——这正是拆接口而不是加方法的原因。

**边界**：`InflightLimiter` 只是让能力**可发现**，它不能强制调用方真的调用 `Done`。漏调用仍然会锁死（`bbr` 冷启动时 `maxInflight` 被夹到 1，漏一次就只放行一个请求）。要防这个，只能在中间件层用 `defer` 把配对写死。

### 实现一：`tokenbucket`——零依赖的默认选择

```go
l, err := tokenbucket.New(100, 200, tokenbucket.WithClock(mockClock)) // 100 req/s，突发 200
if err != nil {
	return err
}
defer l.Close()

if ok, _ := l.Allow(); !ok {
	// 超限
}
```

`New(rate, burst float64, opts ...Option) (*Limiter, error)`。`rate <= 0 || burst <= 0` 返回 `ErrInvalidConfig`（`tokenbucket.go`）。初始令牌是满桶（`tokens: burst`），所以**启动瞬间允许一个 burst 的突发**——这是有意的，也是它与「严格匀速」的分界线。

内部是惰性补充：不跑后台 goroutine，每次 `takeLocked` 按 `now - last` 折算令牌数（`tokenbucket.go`）：

```go
elapsed := now.Sub(l.last).Seconds()
if elapsed > 0 {
	l.tokens = math.Min(l.burst, l.tokens+elapsed*l.rate)
	l.last = now
}
```

**代价**：`l.last` 只在 `elapsed > 0` 时推进，所以时间倒流（注入的时钟回拨）不会退令牌，只会让 `last` 停在原地等真实时间追上来。这是防呆，不是 bug。

`Wait` 先试一次，不够就睡精确算出的 `wait` 再重试（`tokenbucket.go`）。因为等待时长是算出来的而不是猜的，正常路径一次循环就够，不产生轮询开销。

`WithClock` 的语义是**只影响状态演进**：`tokenbucket` 用它折算令牌补充，`bbr` 用它决定桶环旋转；二者都接受注入的时钟。`sentinel` 不涉及时间，没有该 Option。对照 `retry.WithClock`——那里只影响 `maxTotalWait` 的 elapsed 计算，睡眠仍走真实计时器。

### 实现二：`bbr`——自适应，且必须成对调用

```go
var l ratelimit.InflightLimiter = bbr.New(
	bbr.WithCPUThreshold(0.8),
	bbr.WithWindow(5*time.Second),
)
defer l.Close()

start := time.Now()
if ok, _ := l.Allow(); ok {
	defer func() { l.Done(time.Since(start)) }()
}
```

`bbr.Limiter` 同时满足 `ratelimit.Limiter` 与 `ratelimit.InflightLimiter`（`bbr.go` 有两条编译期断言钉住）。**这就是拆接口的直接收益**：`Done` 本来就在具体类型上，签名与 `InflightLimiter` 正好对上，`bbr` 一行实现代码都不用改。

`bbr.New(opts ...Option) *Limiter`——**不返回 error**。四个 Option 全部对非法值静默忽略（`WithCPUThreshold` 只在 `0 < t < 1` 时生效，`bbr.go`；`WithWindow` 只在 `d > 0` 时生效；`WithBucketCount`、`WithMinQPS` 同理）。传 `WithCPUThreshold(1.5)` 不会报错，只会拿到默认的 0.80。

算法是滑动窗口 + 环形桶：窗口默认 10 秒、切成 40 个桶（每桶 250ms），`Done(rtt)` 往当前桶累加 `count` 与 `totalRTT`，`Allow` 每次先旋转桶环再估算（`bbr.go` 的 `rotateLocked`）。

**必须说清的口径**：包注释（`bbr.go`）说 `maxQPS = windowSize / minRTT`，但 `estimateMaxQPSLocked`（`bbr.go`）实际算的是 `min(观测QPS, window/minRTT)`，其中观测 QPS 是 `totalCount / window`。按 Little's law，观测吞吐本来就 ≤ `window/minRTT`，取 min 之后这个值基本等于**观测 QPS**，而不是「最大 QPS」。名字与实现不一致，读代码时以实现为准。

**边界**：`bbr` 的正确用法是「`Allow` 与 `Done` 成对」。只调 `Allow` 不调 `Done` 会把 `inflight` 永久占住，冷启动时 `maxInflight` 被夹到 1，于是**只放行一个请求，之后全是拒绝**。`InflightLimiter` 让 `Done` 可从接口类型够到，但**配对仍然靠调用方自觉**——`bbr_test.go` 的 `TestAllow_DoneReleasesInflight` 与 `TestAllow_UnpairedAllowLocksOut` 把这两种行为都钉住了，`TestWait_HoldsSlotUntilDone` 钉住 `Wait` 侧的同一义务（成功的 `Wait` 也占槽，也须配 `Done`）。

### 实现三：`sentinel`——桥接，不做规则管理

```go
sentinel.InitDefault()                    // 全局初始化，一次
flow.LoadRules([]*flow.Rule{{ /* ... */ }}) // 规则自己配

l := sentinel.New("my-api")
defer l.Close()

if ok, _ := l.Allow(); !ok {
	// 被 Sentinel 拦下
}
```

`sentinel.New(resource string, opts ...Option) *Limiter`，同样不返回 error。它**只管 Entry/Exit 生命周期**，规则（阈值、策略、熔断、系统保护）全部留给调用方通过 Sentinel 自己的 `flow.LoadRules` / datasource API 配置——这是刻意的分工：规则管理是 Sentinel 的强项，重写一遍没有收益。

三条 Option：`WithTrafficType`（默认 `base.Inbound`）、`WithEntryOptions`（透传原生 entry option）、`WithWaitInterval`（`Wait` 的轮询间隔，默认 10ms）。

**边界（四条，都要当真）**：

1. `Close()` 是 no-op（`sentinel.go`）。Sentinel 有自己的生命周期，适配器不越权。
2. `Wait` 拿到放行后会立刻 `e.Exit()`（`sentinel.go`），注释自己承认：「因为我们不知道调用方的工作何时结束」。**所以并发型规则经 `Wait` 走一遍，计数不生效。**
3. 规则按 `resource` 名全局共享。两个 `sentinel.New("my-api")` 拿到的是同一份规则，不是两个独立的限流器。
4. **它不是 `InflightLimiter`**。`Allow()` 把 entry 句柄直接丢弃（`sentinel.go`），并发型规则只能走 `AllowEntry()`/`ReleaseEntry(e)`。契约的 `Done(rtt)` 不带句柄，所以这里没有可实现的形状；`sentinel.go` 的包注释按此写明。

### 三者的边界对照表

| | `tokenbucket` | `bbr` | `sentinel` |
|---|---|---|---|
| 契约接口 | `Limiter` | `Limiter` + **`InflightLimiter`** | `Limiter` |
| 构造签名 | `New(rate, burst, opts) (*Limiter, error)` | `New(opts) *Limiter` | `New(resource, opts) *Limiter` |
| 非法配置 | 返回 `ErrInvalidConfig` | 静默忽略 | 静默忽略 |
| 可注入时钟 | 是（`WithClock`） | 是（`WithClock`） | 不涉及 |
| 释放方式 | 不需要 | `Done(rtt)`，**已进接口** | `ReleaseEntry(e)`，句柄式，未进接口 |
| 第三方依赖 | 无 | 无 | `sentinel-golang v1.0.4` |
| 测试 | 11 个 | 6 个 | 6 个 |

这张表就是本文档存在的主要理由之一：**同一份契约下的三个实现，使用形状并不一致，而这些不一致必须写在类型上而不是藏在注释里。**

## 理由与取舍

### 我们没选 `golang.org/x/time/rate` 作为契约

`rate.Limiter` 是 Go 生态事实上的标准令牌桶，成熟、有 `Reserve`/`AllowN`/`WaitN`、有 `SetLimit` 动态调速。我们没用它当契约，三个原因：

1. **它是实现，不是契约**。`x/time/rate` 只覆盖「固定速率 + 突发」，`bbr` 的自适应与 `sentinel` 的规则引擎都套不进去。把 `rate.Limiter` 当接口用，等于宣布「限流只能是令牌桶」。
2. **它的 `Wait` 语义与我们要的不完全一样**。`rate.Limiter.Wait` 在暂时超限时同样阻塞等待，只在 `n > burst`（永不可能满足）或等待超过 ctx deadline 时返回错误；`ratelimit.Wait` 则把「永久枯竭」表达为 `ErrLimited` 哨兵（`ratelimit.go`），不依赖调用方传 deadline 才能感知。此外 `x/time/rate` 没有 `Close` 生命周期，「已关闭」这一状态在它的类型上无法表达。
3. **它会把零依赖破掉**。`x/time/rate` 自身零依赖，但它属于 `x/` 扩展库；`tokenbucket` 手写 156 行就拿到了同样的语义，且带 `WithClock`。

**代价我们承认**：手写的令牌桶没有 `x/time/rate` 那样的 `SetLimit` 动态调速、`ReserveN` 预占、`AllowN` 批量消耗，也没有它十年积累的边界处理。要这些能力就得自己加扩展点。

### 我们没选「一个包塞三种算法 + build tag」

三种算法共享一个包、用 build tag 或 `driver` 字符串分发，看起来模块更少。否决理由：

- **依赖会传染**。`sentinel` 拖着 `sentinel-golang` 及 18 个 indirect（`go.mod`）。塞进同一个包后，只想用令牌桶的人也得把 Sentinel 拉进依赖树——这就违背了「零件」定位。
- **零依赖是给默认实现留的**。`tokenbucket` 与 `bbr` 的 `go.mod` 都只有 `require ratelimit v0.0.0` + `replace ..`，编译它们不需要网络。这是 `health`/`retry`/`registry` 一脉相承的惯例。
- **独立 module = 独立版本**。`sentinel` 跟随上游 SDK 升级时，不必牵动令牌桶的版本号。

### 我们把 `Done` 放进一个新接口，而不是放进 `Limiter`

这是本文档最该讲清的一处取舍。

把 `Done()` 加进 `Limiter` 的代价：`tokenbucket` 必须实现一个空方法，所有调用点都要记得成对调用，而忘记调用时——对令牌桶无害、对 `bbr` 是死锁。**一个在多数实现上是 no-op、在少数实现上是致命的方法，是最容易被忘掉的方法。**

不加的代价：并发型限流器无法通过接口使用，只能把具体类型泄漏给调用方——`Done` 只存在于 `*bbr.Limiter` 上，而调用方拿到的静态类型是 `ratelimit.Limiter`，编译期就够不着。

我们选的是第三条路：**拆成两个接口**，让「需要释放」变成类型系统能表达的事。

```go
type Limiter interface {
	Allow() (ok bool, err error)
	Wait(ctx context.Context) error
	Close() error
}

// InflightLimiter 在 Limiter 之上要求成对释放。
type InflightLimiter interface {
	Limiter
	Done(rtt time.Duration)
}
```

`bbr` 实现 `InflightLimiter`，**零代码改动**——它本来就有 `Done(rtt time.Duration)`，签名正好对得上。调用方 `if il, ok := l.(ratelimit.InflightLimiter); ok { ... il.Done(time.Since(start)) }`：显式、可发现、不改任何已有实现，也不给 `tokenbucket` 强加空方法。

**为什么 `Done` 带 `rtt` 而不是空参数**：空参数版本会逼 `bbr` 把 `Done(rtt)` 改名为 `DoneWithRTT(rtt)` 再加一个 `Done()`，接口反而说不清「`bbr` 的正确用法是哪个」。带 `rtt` 的唯一代价是「不需要它的实现得忽略一个参数」——契约里已经把这句话写明。**选择让接口多一个参数，而不是让 `bbr` 多一个方法。**

### 我们没让 `sentinel` 也实现 `InflightLimiter`

`sentinel` 的释放是句柄式的（`ReleaseEntry(e)`），要进接口就得让 `Allow` 返回句柄，而 `Allow` 的签名属于基础契约。为一种实现改基础契约的形状，代价大于收益。

所以我们保留了两条路并存：`Allow()` 做一次性放行（QPS 规则），`AllowEntry()`/`ReleaseEntry(e)` 做成对放行（并发规则）。**代价是「同一类型的两种用法不能混」**——调用方得知道自己的规则是 QPS 还是并发型。我们把这条差异写进类型与注释，而不是让 `Done` 假装能覆盖它。

### 我们没让 `bbr` 去读真实 CPU

`WithCPUThreshold` 的名字容易被读成「CPU 使用率阈值」，但代码里没有任何 CPU 采样——阈值只是 `maxInflight = maxQPS * threshold` 的乘数。这是刻意的：读 `/proc/stat` 或 `gopsutil` 会引入平台依赖与采样噪声，也会把 `bbr` 从零依赖打成有依赖。**我们把它当成「inflight 的保守系数」，不是「负载传感器」。** 需要真实系统负载时用 `sentinel` 的系统自适应规则。

## 契约边界

### 1. `ErrLimited` 同时表示「超限」与「已关闭」（未修）

`tokenbucket.Allow()` 在 `closed` 时返回 `ErrLimited`，`bbr` 同样。调用方无法区分「该退避重试」和「该停止发送」。拆哨兵是破坏性变更，留待有调用方时再定。

### 2. `tokenbucket.Close()` 不唤醒 `Wait`（未修）

`Close` 只置 `closed = true`；`Wait` 的 `select` 里虽然有 `case <-l.notify`，但 `notify` 从创建后从未被写入——等待者要等自己那轮 `time.After(wait)` 到期才会重新看到 `closed`。注释与代码必须有一个改：要么在 `Close` 里关闭/通知 channel，要么改注释。`TestClose_WaitReturnsErrAfterClose` 通过是因为 `New(1, 1)` 的 `wait` 恰好 1 秒，测试等满 1 秒后才重入循环——它通过了，但不是因为 Close 生效。

### 3. 没有存量调用点

四个 module 均未被任何其他 module require。要落地的是**接入**：把 `tokenbucket` 挂到 HTTP 中间件链、让 `bconf` 的 `rate_limit` 字段真正生效。接入前必须先决定 `wait` 字段映射到 `Allow` 还是 `Wait`——对客户端是两种不同行为（立刻 429 vs 排队变慢），不能靠默认值糊过去。

## 实现与验证

`ratelimit` 根 module（契约 + `InflightLimiter` + 测试）+ `tokenbucket`/`bbr`/`sentinel` 三个实现 module，共 4 个 module。测试 27 个：根 4 + `tokenbucket` 11 + `bbr` 6 + `sentinel` 6。

`Taskfile.yml` 的 `ratelimit-verify` 逐 module 挂入 `verify`；根 `README.md` 已登记模块树；CI 用 `find . -name go.mod` 自动发现全部 module。

**三个 `WithClock` 的语义不同（刻意）**：`tokenbucket`/`bbr` 的时钟决定**状态演进**（令牌补充、桶旋转），而 `retry.WithClock` 只影响 `maxTotalWait` 的 elapsed 计算（睡眠仍走真实计时器）。

### 验证

```bash
cd ratelimit && go vet ./... && go test -count=1 ./...              # 4
cd ratelimit/tokenbucket && go vet ./... && go test -count=1 ./...  # 11
cd ratelimit/bbr && go vet ./... && go test -count=1 -v ./...       # 6
cd ratelimit/sentinel && go vet ./... && go test -count=1 ./...     # 6
```

## 接入顺序

1. **HTTP 中间件**：位置在 `pkg/middleware/gin/ratelimit.go` + `bundle` 链，读 `bconf` 的 `server.http.middleware.rate_limit`。链序放在 `Recovery` 之后、`Authn` 之前——限流是保护自己的，不该等到认证之后才生效；这也意味着它必须能处理「未认证的洪水流量」。
2. **gRPC 侧**：`bconf` 的 gRPC `Middleware` 段目前只有 `recovery`/`logging`/`tracing`，没有限流字段——要加就得动契约。

中间件里用 `bbr` 时，配对必须只挂在放行成功的分支上：

```go
if il, ok := limiter.(ratelimit.InflightLimiter); ok {
	start := time.Now()
	if ok, err := il.Allow(); err != nil || !ok {
		c.AbortWithStatus(http.StatusTooManyRequests)
		return
	}
	defer func() { il.Done(time.Since(start)) }()
}
c.Next()
```

## 附录

### 完整 API

| module | 类别 | 导出符号 | 默认值 |
|---|---|---|---|
| `ratelimit` | 接口 | `Limiter{Allow, Wait, Close}` | — |
| | 接口 | `InflightLimiter{Limiter, Done(rtt)}` | — |
| | 哨兵 | `ErrLimited` | — |
| `tokenbucket` | 构造 | `New(rate, burst float64, opts ...Option) (*Limiter, error)` | — |
| | 配置 | `WithClock(func() time.Time)`（补） | `time.Now` |
| | 哨兵 | `ErrInvalidConfig` | — |
| | 方法 | `Allow`、`Wait`、`Close` | — |
| `bbr` | 构造 | `New(opts ...Option) *Limiter` | — |
| | 配置 | `WithCPUThreshold(float64)` | `0.80` |
| | | `WithWindow(time.Duration)` | `10s` |
| | | `WithBucketCount(int)` | `40` |
| | | `WithMinQPS(float64)` | `1.0` |
| | 方法 | `Allow`、`Wait`、`Close`、`Done(rtt)`、`MaxInflight()`；**实现 `InflightLimiter`** | — |
| `sentinel` | 构造 | `New(resource string, opts ...Option) *Limiter` | — |
| | 配置 | `WithTrafficType(base.TrafficType)` | `base.Inbound` |
| | | `WithEntryOptions(...sentinelapi.EntryOption)` | — |
| | | `WithWaitInterval(time.Duration)` | `10ms` |
| | 方法 | `Allow`、`Wait`、`Close`、`AllowEntry()`、`ReleaseEntry(e)` | — |

### FAQ

**为什么 `Allow` 返回 `(bool, error)`，`Wait` 只返回 `error`？** `Allow` 的两个返回值是「放行了吗」和「为什么没放行」；`Wait` 成功就是成功，失败只可能是 `ctx` 取消或限流器关闭，都在 error 里。

**为什么 `bbr.New` 不返回 error，`tokenbucket.New` 返回？** 参数形态不同：`tokenbucket` 有两个必填数值参数（`rate`、`burst`），非法值必须在构造期拦下；`bbr` 全是可选 Option，当前实现对非法值静默忽略。**这是已知缺陷而不是设计**——见「开放问题」第 1 条。

**`Done` 为什么带一个 `rtt` 参数，而不是空参数？** 因为 `bbr` 需要它来估算可持续放行量。空参数会逼 `bbr` 把 `Done(rtt)` 改名再加一个 `Done()`，接口反而说不清正确用法。不需要 `rtt` 的实现忽略它即可——`tokenbucket` 干脆不实现这个接口。见「理由与取舍」。

**我拿到的是 `ratelimit.Limiter`，怎么知道要不要调 `Done`？** 一次类型断言：`if il, ok := l.(ratelimit.InflightLimiter); ok { defer il.Done(time.Since(start)) }`。断言成立就必须配对（成功的 `Wait` 同样占用，也须配 `Done`），不成立就没有可释放的东西。

**`bbr` 的 `cpuThreshold` 是 CPU 使用率吗？** 不是。它只是 `maxInflight` 的乘数，代码里没有任何 CPU 采样。见「理由与取舍」。

**`tokenbucket` 启动时为什么能一次放行 `burst` 个请求？** 因为初始令牌是满桶（`tokens: burst`）。要「启动即匀速」就把 `burst` 设为 1。

**`sentinel` 的 `Close()` 为什么什么都不做？** Sentinel 是全局单例式的，规则与统计都挂在全局注册表上；适配器只持有一个 resource 名，没有可释放的东西。**这也意味着 `Close()` 之后 `Allow()` 依然工作。**

### 开放问题

1. **`bbr` / `sentinel` 的构造签名要不要统一成返回 error？** 统一意味着 `bbr.WithCPUThreshold(1.5)` 从「静默用默认值」变成「构造失败」。这是行为变更，但对「配置写错要 fail-fast」的既有原则更自洽。
2. **`ErrLimited` 要不要拆成两个哨兵？** 例如 `ErrLimited`（超限）+ `ErrClosed`（已关闭）。调用方需要区分「退避重试」和「停止发送」时，现在的单哨兵不够用。属于破坏性变更，需要单独决策。
3. **`tokenbucket` 的 `notify` 死字段怎么处理？** 删掉，还是实现「Close 时唤醒等待者」？后者要小心——关闭已关闭的 channel 会 panic，正确做法是 `close(done)` + `select`。现状是注释承诺了未实现的行为，注释与代码必须有一个改。
4. **`sentinel` 要不要也走 `InflightLimiter`？** 那需要把 `Allow` 改成返回句柄，动的是基础契约。当前折中是「句柄式释放留在具体类型上」，代价是调用方得知道自己的规则是 QPS 还是并发型。
5. **HTTP 中间件的 `wait` 字段映射到什么？** `Allow`（立刻 429）与 `Wait`（排队变慢）对客户端是两种行为。`bconf` 里这个 `bool` 必须有一个明确语义，不能靠默认值。
