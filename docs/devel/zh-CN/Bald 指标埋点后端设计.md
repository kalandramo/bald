# bald 指标埋点后端：metrics.Metrics 三原语接口与三个可替换后端

> Title: bald 指标埋点后端：metrics.Metrics 三原语接口与三个可替换后端
>
> Author(s): bald 团队
>
> 适用包：`metrics/`、`metrics/prometheus`、`metrics/otel`、`metrics/datadog`
>
> 关联文档：[指标抽象设计](./指标抽象设计.md)（`pkg/metrics` Recorder——请求级指标，与本文是**并存的两套体系**，见「背景与动机」的双体系事实地图）、[审计抽象设计](./审计抽象设计.md)（旁路纪律同源）、[Bald Bootstrap 设计](./Bald%20Bootstrap%20设计.md)（契约装配链）
>
> Last updated: 2026-09-15
>
> Status: Accepted（现状记录：本文逆向生成自代码实现，所有行为以代码为准，含已知缺陷清单）

## 摘要

顶层 `metrics/` 子模块定义**传输级埋点契约**：`Metrics` 三原语接口（Counter / Histogram / Gauge）+ 可选 `Closer`，供 TCP / Kafka / RabbitMQ / Redis / RocketMQ / WebRTC 六个消息服务器在连接与消息处理路径上手工埋点。契约模块零第三方依赖（`go.mod` 无任何 require），后端以三个独立子模块提供（Prometheus / OTel / Datadog），业务经 transport 的 `WithMetrics` 注入。

最重要的两个事实，读者先知道再往下读：

1. **它和 `pkg/metrics`（Recorder）是两套并存的体系**，不是一套的两层。前者管消息服务器埋点（自由标签、手工调用），后者管 HTTP/gRPC 请求级指标（审计同源维度、中间件自动 emit、走 bconf 契约装配链）。「背景与动机」的双体系事实地图给出对照与重叠代价。
2. **四个模块至今零 tag、从未发布**。`transport/tcp`、`transport/webrtc` 的 go.mod 里 require 的 `metrics v0.0.1` 是不存在的编造版本。发版前置清理见「实现与过渡」的 checklist。

## 背景与动机

### 消息服务器没有「请求」，请求级 Recorder 覆盖不了它们

`pkg/metrics` 的 Recorder 把维度固化为 `(transport, object, action, result)`——这是 HTTP/gRPC 中间件的语义。但 TCP 会话与 MQ 消费循环里没有「请求」，维度天然是 `status`、`topic`、`queue` 这类自由标签。`transport/tcp` 的真实埋点代码（`server.go:373-394`，nil 保护 + 三原语混用）：

```go
if s.m != nil {
    s.m.Counter(ctx, "tcp_messages_total", 1, map[string]string{"status": "error"})
}
...
if s.m != nil {
    latency := time.Since(start).Seconds()
    s.m.Counter(ctx, "tcp_messages_total", 1, map[string]string{"status": "ok"})
    s.m.Histogram(ctx, "tcp_message_duration_seconds", latency, map[string]string{})
}
```

连接生命周期用 Gauge 增量维护在途连接数（`server.go:426` / `:462`）：

```go
s.m.Gauge(context.Background(), "tcp_connections_in_flight", 1,  map[string]string{})  // 连接建立
s.m.Gauge(context.Background(), "tcp_connections_in_flight", -1, map[string]string{})  // 连接断开
```

这类「名字与标签由埋点方决定」的需求，就是 `metrics.Metrics` 存在的理由。

### 双体系的事实地图

| | `pkg/metrics`（Recorder） | `metrics/` 子模块（Metrics 三原语） |
|---|---|---|
| 定位 | 请求级（HTTP/gRPC） | 传输级（TCP/MQ/WebRTC 消息与会话） |
| 维度 | 固定：transport/object/action/result（审计同源） | 自由：埋点方传 name + labels |
| 指标 | 固定两条：`bald_requests_total`、`bald_request_duration_seconds` | 埋点方命名（tcp/kafka 各自一套） |
| emit 方式 | 中间件自动（`AuditWithMetrics`） | 业务/传输代码手工调用 |
| 后端装配 | bconf `metrics` 段 → appkit `MetricsRegistry` → `contrib/observability-otlp/contract` → `Setup` 多 Reader | 业务自选后端 `New()` 直连，**不经契约** |
| otel 依赖 | 契约层即 otel API（全局 MeterProvider） | 契约层零依赖，otel 只是三后端之一 |
| panic 防护 | `emitMetricsSafely` recover 降级 | 无（tcp 直接调用；当前三后端不主动 panic，风险低） |
| 发版 | 随根模块 v0.6.1 | **零 tag** |

两套体系共享一个全局单例位：`otel.SetMeterProvider`。`metrics/otel.New` 与 `observability-otlp` 的 `Setup` 都会设它，同时使用时后设者覆盖前者——被顶掉的一侧所有指标静默丢失（详见 §5.3）。

## 设计

### 1. 契约：三原语接口，模块零依赖

`metrics/metrics.go` 全部内容就是两个接口：

```go
type Metrics interface {
    Counter(ctx context.Context, name string, value float64, labels map[string]string)
    Histogram(ctx context.Context, name string, value float64, labels map[string]string)
    Gauge(ctx context.Context, name string, value float64, labels map[string]string)
}

type Closer interface {
    Close() error
}
```

三点边界，均以代码为准：

- **无返回值**：埋点永不向上游报错，失败静默（旁路纪律，与 audit.D5 同源）。代价是「可观测性自身不可观测」——后端挂了你只能在指标端点上发现数据没了。
- **`ctx` 由后端自行处置**：otel 后端把 ctx 传给 `Add`/`Record`（参与 OTel 上下文传播）；prometheus 与 datadog 后端显式 `_ = ctx` 丢弃。
- **`Closer` 是能力探测接口而非生命周期要求**：持有后台资源（OTLP 缓冲、UDP 连接）的后端实现它（otel、datadog 有 `var _ metrics.Closer` 断言），prometheus 无需 Close。调用方用类型断言决定是否挂停机。

`metrics/go.mod` 只有 module 声明与 `go 1.27.1`，是全仓最纯的契约模块——比 berrors 根包（零第三方依赖但属根模块）更进一步：整个模块可被任何后端零成本引入。

### 2. 模块布局：一个契约，三个后端

```
metrics/                  module github.com/kalandramo/bald/metrics          （零 require）
├── prometheus/           module …/metrics/prometheus  → client_golang v1.23.2
├── otel/                 module …/metrics/otel        → otel v1.46.0 全家（grpc/http exporter、sdk/metric）
└── datadog/              module …/metrics/datadog     → DataDog/datadog-go/v5 v5.8.2
```

三个后端统一形态：`New(opts ...Option) (*Provider, error)` + `var _ metrics.Metrics = (*Provider)(nil)` 断言 + functional options。命名对齐六域桥接模块的顶层布局判别（独立 go.mod = 可独立发布），但这四个模块**都还没有发过版**。

### 3. Prometheus 后端：promauto 工厂 + 七个惰性缓存 map

`Provider` 持有 cfg、`promauto.Factory` 与七个 map（counters/counterVecs/histograms/histogramVecs/gauges/gaugeVecs/labelNames）。instrument 首次使用时创建并缓存；无标签走单 instrument，有标签走 `*Vec`。

有两个构造器，行为差异是刚性的：

- `New()`（`prometheus.go:81-82`）：**无条件 `reg := prometheus.NewRegistry(); cfg.registry = reg`**——传入的 `WithRegistry` 被覆盖丢弃。每个 Provider 一个私有注册表，互相隔离；`Registry()` 返回它供 `promhttp.HandlerFor` 挂自有端点。
- `NewWithDefaultRegistry()`：尊重 `cfg.registry`（缺省 `prometheus.DefaultRegisterer`），指标混入进程全局注册表，`promhttp.Handler()` 可直接暴露。

注意这是**现状缺陷而非设计**：`WithRegistry` 的注释写着 "useful for testing"，但它对 `New()` 无效。「实现与过渡」缺陷清单 D2 有处置建议。

另一个必须知道的边界：**label keys 首见冻结**（`cachedLabelKeys`，`prometheus.go:144-151`）。同一 name 第一次出现的 label key 集合被缓存为该指标的 Vec 维度，之后传入不同（更多）label 的键会被静默丢弃而非报错。

### 4. OTel 后端：OTLP 直推 + 全局 MeterProvider

`New()` 组装链：OTLP exporter（gRPC 缺省 / `WithHTTP` 切换）→ resource（semconv v1.34.0 的 ServiceName/Version）→ PeriodicReader（缺省 60s）→ `MeterProvider` → **`otel.SetMeterProvider(mp)` 设为全局**。三 map 有 `sync.Mutex` 保护（prometheus 后端没有，见缺陷清单 D1），instrument 创建失败静默 return。`Close()` 以 10s 超时 `Shutdown` flush 尾批。

**Gauge 在此退化为 `Float64UpDownCounter`**（`otel.go:210-213`，代码注释自认）：OTel 原生 gauge 是回调式 observable，命令式接口没有对应物，这里用 Add 语义模拟。这个妥协的代价在「兼容性」的 Gauge 语义分歧一节展开——它让 Gauge 的语义跨后端不再一致。

缺省 `serviceName = "bald-app"`（`otel.go:56`；2026-09-15 自 go-wind 移植残留 `"go-wind-service"` 清理，对齐 `observability-otlp` 的 `defaultServiceName` 惯例）。

### 5. Datadog 后端：DogStatsD UDP

最薄的一层：`statsd.Client` 直连 `127.0.0.1:8125`，labels 转 `k:v` tag，`rate` 支持采样。两处边界：`Counter` 用 `int64(value)` 截断浮点增量；`WithFlushPeriod` 声明了但 `New()` 未消费（`clientOpts` 只接 namespace 与 bufferSize）——声明的 Option 是死配置。

### 6. 消费方式与既有埋点清单

六个传输模块经 `WithMetrics(m metrics.Metrics)` 注入（与 `WithTracer` 对称）。已落地的埋点（实测代码）：

| 传输 | 指标 | 类型 | 维度 |
|---|---|---|---|
| tcp | `tcp_messages_total` | Counter | status ∈ {ok, error} |
| tcp | `tcp_message_duration_seconds` | Histogram | — |
| tcp | `tcp_connections_in_flight` | Gauge ±1 | — |
| kafka | `broker.messages.received` | Counter | broker, topic 等 |
| kafka | `broker.message.duration` | Histogram | 同上 |
| kafka | `broker.messages.errors` | Counter | broker, topic |

命名惯例已经分裂：tcp 系按 Prometheus 惯例（下划线、`_total`/`_seconds` 后缀），kafka 系按 DogStatsD 惯例（点分隔）。这不是风格问题：Prometheus 指标名字符集不含点号，kafka 系埋点在 prometheus 后端下抓取端会拒绝解析。

### 7. 与契约装配链的关系：不经 bconf，datadog 无契约位

bconf 的 `metrics` 段（`type ∈ {"prometheus", "otlp"}`）驱动的是 `pkg/metrics` 那条链（appkit `MetricsRegistry` → `observability-otlp/contract` → `Setup`），**与本接口无关**。本体系的三个后端只能业务手工 `New()` 后注入 transport；datadog 在契约里没有取值，永远走不了配置驱动。

> 顺带指出一处既有文档漂移：《指标抽象设计.md》§5 写「contract Provider 收 `func() string`，Build 时才取契约 app.name」，现行代码是 `NewPrometheusProvider(serviceName string)` 即时绑定 string（`contract.go:64`）。以代码为准，该文档此句待更新。

## 理由与取舍

**决策①：两套指标体系为什么并存，而不是合并成一套？** 合并的两个方向我们都否决过。向上合并（Recorder 泛化为自由标签）会丢掉「与审计三元组同源」的结构保证——维度一自由，`(secret, get)` 在指标里就可能变成 `(secrets, read)`，按指标排查对不上审计详情（M7 的教训）。向下合并（Metrics 收敛为固定维度）则消息服务器的 topic/queue/status 全部塞不进 object/action 模型。重叠的代价我们如实承认：双全局 MeterProvider 抢位、双套命名空间、后端配置两套——这是「兼容性」全局单例位互斥约束的根源，长期看值得收敛为「共享 Provider、两套语义层」，但那是独立提案，不混入现状记录。

**决策②：接口为什么无错误返回？** 埋点是旁路：后端连不上 Collector、UDP 丢包、instrument 创建失败，都不该影响消息处理主路径。这与 `audit.Auditor` 的 D5 纪律对称。代价是静默丢数，且没有自身的健康信号——我们接受，因为指标链路的排障入口是「端点上没数据」这个现象本身。

**决策③：Gauge 为什么在 otel 后端退化为 UpDownCounter？** OTel 的 gauge 是 observable（回调注册、周期采集），把它接进命令式接口需要持有可变状态 + 注册回调生命周期，复杂度远超收益；`Float64UpDownCounter` 的 Add 语义在「增量维护」的用法下等价。但代价必须说透——见下节，它造成了跨后端行为分歧。

**决策④：后端为什么放顶层子模块而不是 contrib/？** 对齐布局判别：独立 go.mod = 可独立发布的桥接模块，且契约（零依赖）与后端（重依赖）必须物理隔离，否则 transport 六模块会被迫传递引入 client_golang/otel/datadog-go。contrib/ 语义是「接根模块运行期框架」，这三者不接任何框架，只是 SDK 桥接。

**决策⑤：为什么 `New()` 与 `NewWithDefaultRegistry()` 是两个函数？** 如实地说，现状不是好设计：私有注册表隔离（`New`）与全局注册表混用（`NewWithDefaultRegistry`）是两种合法策略，但用两个构造器区分，同时 `New()` 内部又无条件覆盖 `WithRegistry`，三者互相矛盾。我们记录它为缺陷（§6.1 D2），不建议后来者效仿。

## 兼容性

### 零 tag：事实上没有外部用户，但 transport 欠着编造版本

四个模块从未发版，接口可以自由修正而不破坏任何人。但 `transport/tcp`、`transport/webrtc` 的 go.mod require `metrics v0.0.1`——**该 tag 不存在**；`transport/{kafka,rabbitmq,redis,rocketmq}` require 伪版本 `v0.0.0-00010101000000-000000000000` 靠 replace 兜底。当前全靠 `replace ../../metrics` 本地开发在编译；一旦去掉 replace 发布，tcp/webrtc 必然解析失败。

### Gauge 语义跨后端分歧：换后端 = 换行为

接口契约是 Set 语义（`Gauge sets the current value`），三后端的实际行为：

| 后端 | Gauge 实现 | tcp `connections_in_flight` ±1 用法的结果 |
|---|---|---|
| prometheus | `GaugeVec.Set` | Set(1) 与 Set(-1) 互相覆盖，值在 ±1 跳变——**错** |
| datadog | statsd Gauge（Set） | 同上——**错** |
| otel | `UpDownCounter.Add` | 增量累计，正确反映在途数——**对** |

tcp 的增量用法本身偏离接口契约（应传绝对值），otel 的 Add 实现也偏离契约——两个相反方向的偏离恰好只在 otel 组合下互相抵消。这不是理论问题：同一份业务代码，从 otel 切到 prometheus 后端，in_flight 指标就静默坏掉。

### 全局 MeterProvider 单例位：与 observability-otlp 互斥使用

`metrics/otel.New` 与 `observability-otlp/metrics.Setup` 都调 `otel.SetMeterProvider`。同时使用时后设者生效、先设者的全部指标静默丢失；`pkg/metrics` 的 Recorder 依赖全局位，被 `metrics/otel` 顶掉后请求级指标也会路由错误。约束：**一个进程里二选一**。要同时要 OTLP 直推和 Prometheus 抓取，用 `Setup` 的多 Reader（它本来就是为此设计的）。

## 实现与过渡

现状代码即实现（本文为逆向记录）。以下为发版前必须处置的事项。

### 已知缺陷清单（按严重度）

| # | 缺陷 | 位置 | 严重度 |
|---|---|---|---|
| D1 | prometheus Provider 七 map **无锁**，并发埋点 data race（otel 版有 mu，datadog 版无 map 所以幸免；tcp 的 handler 是并发的，prometheus 后端下必现） | `prometheus.go` 全文件 | 高 |
| D2 | `New()` 无条件覆盖 `cfg.registry`，`WithRegistry` 失效 | `prometheus.go:81-82` | 高 |
| D3 | Gauge Set/Add 语义跨后端分歧（§5.2）；tcp 增量用法仅 otel 正确 | 接口 + 三后端 + tcp | 高 |
| D4 | kafka 系点号命名不符合 Prometheus 指标名字符集，prometheus 后端下不可抓 | `transport/kafka` | 中 |
| D5 | `WithFlushPeriod` 声明未消费 | `datadog.go:68-71` | 低 |
| D6 | label keys 首见冻结，后见 label 静默丢弃 | `prometheus.go:144-151` | 低 |
| D7 | datadog `Counter` int64 截断 | `datadog.go:125` | 低 |
| D8 | otel 缺省 serviceName `"go-wind-service"` 血统残留；`doc.go` "go-wind framework" 措辞过期 | `otel.go:56`、`doc.go:2` | 低，已修复（2026-09-15，缺省改 `bald-app`） |

处置原则：D1/D2/D3 应在首个 tag（v0.1.0）前修——零外部用户是修接口语义的最后窗口；D5-D7 可随首版一并清理；D8 已修复；D4 属 transport 埋点规范，建议在六传输内统一 Prometheus 命名。

### 发版 checklist

1. 修 D1-D3（D1 加 `sync.Mutex` 对齐 otel 版；D2 二选一：删覆盖或删 Option；D3 定契约——建议明确 Gauge 为 Set 语义并给 Add 语义正名 `GaugeAdd` 或改用回调，transport 埋点同步改绝对值）。
2. 清理 `doc.go` / 缺省 serviceName 的 go-wind 残留（已完成，2026-09-15：doc.go 改 "bald framework"、缺省 serviceName 改 `bald-app`）。
3. 打 tag：`metrics/v0.1.0` + `metrics/{prometheus,otel,datadog}/v0.1.0`（lightweight，对齐仓库惯例）。
4. transport 六模块 require 改真实版本，replace 保留（发版随迁惯例；新 tag 撞 sumdb 收录延迟用 `GONOSUMDB` 绕过）。

## 附录：FAQ

**Q：业务代码该用 `pkg/metrics` 还是 `metrics/`？** HTTP/gRPC 服务用前者（中间件自动，配 bconf `metrics` 段即得双通道）；自起消息服务器（TCP/MQ）用后者（`WithMetrics` 注入，标签自定）。两者可以共存于一个进程，但 otel 侧只能有一个 Provider（见「兼容性」全局单例位一节）。

**Q：datadog 后端怎么接？** 只能手工：`datadog.New(datadog.WithAddress(...))` 后 `WithMetrics` 注入。契约段无 `datadog` 取值，配置驱动走不通——若需要，是 bconf `Metrics.Type` 枚举的独立增量提案。

**Q：为什么不是六传输共用一套埋点指标名？** 没有强制层：`metrics.Metrics` 对命名零约定，各传输移植时各自带了原生态的惯例（「设计」§6 埋点清单表内的分裂由此而来）。收敛需要一份埋点命名规范，目前以文档记录现状为先。
