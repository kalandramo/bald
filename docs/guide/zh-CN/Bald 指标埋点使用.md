# Bald 指标埋点使用

面向使用者的操作手册：什么时候用这套体系、选哪个后端、怎么注入 transport、
自定义埋点怎么写、换后端要付出什么代价。设计论证（为什么两套体系并存、
决策①~⑦、缺陷清单 D1-D8 及修复状态）见 `docs/devel/zh-CN/Bald 指标设计.md`，
本文只讲操作与场景。

## 心智模型：一句话版

**自起消息服务器（TCP/MQ/WebRTC）→ 选一个后端（手工 `New` 或
`instrumentation` 段装配）→ `WithMetrics` 注入；HTTP/gRPC 请求级指标走
`pkg/metrics`（bconf `metrics` 段契约装配），别用这套。**

```go
m, _ := prometheus.New(prometheus.WithNamespace("myapp"))   // 选一个后端
srv := tcp.NewServer(
    tcp.WithAddress(":9000"),
    tcp.WithSocketRawDataHandler(handler),
    tcp.WithMetrics(m),                                      // 注入，埋点自动生效
)
```

## 0. 使用场景：两套体系怎么选

bald 有**两套并存的指标体系**，不是一套的两层。选错体系 = 白干：

| | `pkg/metrics`（Recorder） | `metrics/`（Metrics 三原语，本文） |
| --- | --- | --- |
| 定位 | 请求级（HTTP/gRPC） | 传输级（TCP/MQ/WebRTC 消息与会话） |
| 维度 | 协议维度（semconv v1.43.0）+ 审计三元组（正交两序列） | 自由：埋点方传 name + labels |
| 指标 | `http.server.request.duration`、`http.server.active_requests`、`rpc.server.call.duration`、`bald_audit_events_total` | 埋点方命名（tcp/kafka 各自一套） |
| emit 方式 | 中间件自动（`AuditWithMetrics`） | 业务/传输代码手工调用 |
| 后端装配 | bconf `metrics` 段 → appkit `MetricsProvider` → `observability-otlp/contract` | 两条都行：手工 `New()` 直连，或 bconf `instrumentation.<后端>` 段 → 各后端 `contract` 子包装配（第 3 节） |
| 发版 | 随根模块 | 独立 tag（`metrics/v0.1.1` + 三后端 `v0.1.0`，见第 1 节） |

**场景判断规则：**

- **HTTP/gRPC 服务** → 用 `pkg/metrics`：中间件自动 emit，配 bconf
  `metrics` 段即得双通道。协议指标对齐 OTel semconv（可套社区 dashboard、
  被 APM 自动识别），业务维度走 `bald_audit_events_total`（与审计三元组
  同源）——两个视角正交，基数各自可控（设计见《Bald 指标 semconv 对齐
  设计.md》）。
- **自起 TCP 会话 / MQ 消费循环** → 用本文的 `metrics/`：这些路径里没有
  「请求」，维度天然是 `status`、`topic`、`queue` 这类自由标签，Recorder
  的固定维度塞不下。
- **一个进程两者都有**（既起 HTTP 又起 MQ 消费）→ 两套并存可以，但
  **otel 侧只能有一个 Provider**（见第 6 节互斥约束）。

**后端怎么选：**

| 后端 | 适用 | 暴露方式 |
| --- | --- | --- |
| prometheus | 自建 Prometheus 抓取体系 | 挂 `/metrics` HTTP 端点，Prometheus 定时抓 |
| otel | OTLP 直推 collector（多后端汇聚：Prometheus / Datadog / Grafana Cloud） | 后台周期推送（缺省 60s），无需端点 |
| datadog | 已有 Datadog Agent 的环境 | DogStatsD UDP（缺省 `127.0.0.1:8125`），Agent 转发 |

## 1. 引依赖

契约模块 `metrics` 零第三方依赖；三个后端是独立 module，用哪个引哪个：

```bash
go get github.com/kalandramo/bald/metrics/prometheus   # 或 metrics/otel、metrics/datadog
```

**已发版**：四个 module 均已打 tag——`metrics/v0.1.1` + 三后端各
`v0.1.0`，`go get` 直接可用。仓库内联调未发版改动时，仍可临时
`replace` 到本地路径（如 `replace github.com/kalandramo/bald/metrics => ../bald/metrics`）。

## 2. 三后端速查

| | prometheus | otel | datadog |
| --- | --- | --- | --- |
| module（`github.com/kalandramo/bald/`…） | `metrics/prometheus` | `metrics/otel` | `metrics/datadog` |
| 构造 | `New()` / `NewWithDefaultRegistry()` | `New()` | `New()` |
| 缺省目标 | 私有注册表 | `localhost:4317`（gRPC） | `127.0.0.1:8125`（UDP） |
| 停机 | 无需 Close | `Close()`：10s 超时 flush 尾批 | `Close()`：flush 并关连接 |
| Gauge 语义 | Set（`GaugeAdd` 增量） | Set（`GaugeAdd` 增量） | Set（`GaugeAdd` 增量） |

### prometheus：两个构造器，行为刚性不同

```go
// 路径 A：私有注册表（New 无条件新建，互相隔离）——用 Registry() 挂自有端点
p, _ := prometheus.New(prometheus.WithNamespace("myapp"))
http.Handle("/metrics", promhttp.HandlerFor(p.Registry(), promhttp.HandlerOpts{}))

// 路径 B：混入进程全局注册表——promhttp.Handler() 直接暴露
p, _ := prometheus.NewWithDefaultRegistry(prometheus.WithNamespace("myapp"))
http.Handle("/metrics", promhttp.Handler())
```

Option：`WithNamespace` / `WithSubsystem`（指标名前缀，最终
`namespace_subsystem_name`）。`WithRegistry` 自定义注册表，对两个构造器
都生效（测试常用）。

### otel：OTLP 直推 + 全局 MeterProvider

```go
m, _ := otel.New(
    otel.WithEndpoint("collector:4317"),     // 缺省 localhost:4317
    otel.WithServiceName("my-service"),      // 缺省 bald-app
    otel.WithInsecure(true),                 // 内网 collector 通常关 TLS
    // otel.WithServiceVersion("v1.2.3"),     // resource 属性，缺省 v0.0.1
    // otel.WithHTTP(true),                  // 切 HTTP 协议（缺省 gRPC）
    // otel.WithExportInterval(30*time.Second), // 缺省 60s
)
defer m.Close()                               // 停机 flush 尾批，别漏
```

`New()` 内部调 `otel.SetMeterProvider` 设全局——**与 `observability-otlp`
的 metrics 后端互斥**（第 6 节）。注意这只发生在两个 **metrics** 后端之间；
trace（`SetTracerProvider`）是另一个独立全局位，不受影响。

### datadog：DogStatsD UDP，最薄一层

```go
m, _ := datadog.New(
    datadog.WithAddress("127.0.0.1:8125"),   // 本机 Datadog Agent
    datadog.WithNamespace("myapp"),          // 指标名前缀
    // datadog.WithSampleRate(0.5),          // 采样率，缺省 1.0 全量
    // datadog.WithBufferSize(1024),         // UDP 批量缓冲
    // datadog.WithFlushPeriod(200*time.Millisecond), // 聚合 flush 间隔，缺省 100ms
)
defer m.Close()
```

## 3. 契约装配：instrumentation 段

后端实例可以不手写 `New()`，改由契约声明、框架在启动期装配——顶层
`instrumentation` 段（第 16 个顶层段）下三个子段各对应一个后端，
**子段存在即选中**：没有 `type` 字符串，与 cache/storage/broker 同构；
多个子段同时存在就产出多个实例，各自落在 `instrumentation.<后端>` 资源路径上。

```yaml
app:
  name: "myapp"          # otlp 的 service_name/version 留空时回落到这里
  version: "v1.2.3"

instrumentation:
  prometheus:                    # 存在即装配 metrics/prometheus
    namespace: "myapp"           # 指标名前缀，最终名 namespace_subsystem_name
    subsystem: "tcp"             # 副前缀，可留空
    use_default_registry: false  # false=私有 Registry（自己挂端点）；true=进程默认注册表
  otlp:                          # 存在即装配 metrics/otel（OTLP 周期导出）
    endpoint: "collector:4317"   # 必填
    insecure: true               # 内网 collector 通常关 TLS
    # service_name: ""           # 留空回落 app.name
    # service_version: ""        # 留空回落 app.version
    # use_http: false            # true = OTLP/HTTP，缺省 gRPC
    # export_interval_seconds: 30  # 0 = 后端默认 60s
  datadog:                       # 存在即装配 metrics/datadog（DogStatsD UDP）
    address: "127.0.0.1:8125"    # 必填
    namespace: "myapp"           # 前缀由后端自动补 "."，不要自带尾点
    # buffer_size: 1             # 单个 UDP 报文最多合包条数，0 = 库默认
    # flush_period_millis: 200   # 客户端聚合节拍（毫秒），0 = 库默认 2s
    # sample_rate: 0.5           # 实测只作用 Histogram，见本节末口径
```

接入姿势与其余域一致——Provider 即 Option，显式出现在你的代码里，没有
`init()` 自注册：

```go
import (
    "context"

    otelcontract "github.com/kalandramo/bald/metrics/otel/contract"
    "github.com/kalandramo/bald/pkg/appkit"
    "github.com/kalandramo/bald/pkg/appkit/std"
    "github.com/kalandramo/bald/transport/tcp"
)

appkit.MustServe(ctx,
    std.Standard(),
    otelcontract.AppProvider(),          // instrumentation.otlp → 资源面
    appkit.Domain{
        Name: "myapp",
        Wire: func(ctx context.Context, app *appkit.App) error {
            // 阶段 B 已就绪、服务器尚未构建——这是官方取用窗口
            if m, ok := otelcontract.Otlp.Resource(app); ok {
                srv = tcp.NewServer(tcp.WithMetrics(m)) // 见第 4 节
            }
            return nil
        },
    },
)
```

（`srv` 是业务自有的变量——把埋点实例交给哪个 transport、什么时候交，本来就是
业务侧的构造时序；这里只演示「从资源面取类型化实例」这一步。）

这条链在真进程里判过：`transport/tcp/contract` 的
`TestAppProviderEndToEnd_InstrumentationSegmentReachesScrapeEndpoint` 起一个契约
`server.tcp` + `instrumentation.prometheus` 的应用，用 `WithAfterBuild` 把段产物交给
`tcp.WithMetrics`（服务器由契约工厂构造，不再是业务自己 `NewServer`），发三帧后在
独立的 `metricshttp` 监听面上抓到 `e2em_tcp_messages_total{status="ok"} 3`。反证腿
`TestAppProviderEndToEnd_MetricsSegmentAbsentFailsServe` 判段缺席时 `Serve` 立即失败并
点名 `instrumentation.prometheus`。

三个包路径与句柄一一对应：`metrics/prometheus/contract` → `contract.Prometheus`、
`metrics/otel/contract` → `contract.Otlp`、`metrics/datadog/contract` →
`contract.Datadog`。句柄是**具体类型**（`*prometheus.Provider` 等），既满足
`metrics.Metrics`（喂 `WithMetrics`），也带各自后端独有的方法（`Registry()`、
`Close()`）。

**为什么单开一个段，不塞进 `metrics` 段**：`metrics` 段驱动的是 `pkg/metrics`
那条请求级 Recorder 链，它的段 Provider 对空/未知 `type` 直接 fail-fast——把
埋点后端挂进那根 `type` 轴上，等于让两套体系共用一条分发键，`prometheus` 这个
取值在两条链上含义不同（一个是 Recorder 的后端，一个是埋点的后端）。子段存在
即选中还顺带消掉了「写错 type 静默无消费」这类洞。

**构造期就挡住的写法**（不留「配了但静默丢包」）：

- `otlp.endpoint` / `datadog.address` 必填。留空时库会静默用
  `localhost:4317` / `127.0.0.1:8125`：构造成功、埋点照常累加，导出失败只出现在
  SDK 后台日志里，进程看起来健康而指标一条都不落地。
- `otlp.export_interval_seconds` ≤0、`datadog.buffer_size`/`flush_period_millis`
  负数、`datadog.sample_rate` 越界（有效域 (0,1]）都判红。
- 段写了但代码里没接 Provider：启动期被 appkit 的 L3 段覆盖检查报错并列出段名
  （反射枚举，新增顶层段自动纳入），不会静默无消费。

**停机与热更**：otel/datadog 的 cleanup 由引擎按逆序回放调用（等价于手工
`Close()`——flush 尾批再关连接），prometheus 的 Registry 是进程生命周期对象、
cleanup 恒为空。本段节点声明 `ReloadRestart`：运行期改这些值不会换实例，
需要重启才生效（埋点后端与导出节拍都是构造期定型的）。

**datadog 的 `sample_rate` 只对 Histogram 生效**：Counter/Gauge/GaugeAdd 走
客户端聚合，聚合器在 flush 时把 rate 硬编成 1（datadog-go v5.8.2 的
`countMetric`/`gaugeMetric` `flushUnsafe`），这三条路径的报文不带 `|@` 标记。
这是后端库的行为，不是配置写法问题；实测钉在
`metrics/datadog/contract/mapping_offline_test.go` 的
`TestMappingOffline_SampleRateAffectsHistogramOnly`，proto 注释里也写了同一条口径。

## 4. 注入 transport：埋点自动生效

五个消息服务器经 `WithMetrics(m)` 注入（与 `WithTracer` 对称），注入后
连接与消息路径上的埋点自动产出，业务代码零改动：

```go
import (
    "github.com/kalandramo/bald/metrics/prometheus"
    "github.com/kalandramo/bald/transport/tcp"
)

p, _ := prometheus.New(prometheus.WithNamespace("myapp"))
srv := tcp.NewServer(
    tcp.WithAddress(":9000"),
    tcp.WithSocketRawDataHandler(handler),
    tcp.WithMetrics(p),          // kafka/rabbitmq/redis/rocketmq 同名 ServerOption
)
```

webrtc 侧是 `WithClientMetrics(m)`（ClientOption，注入客户端）。

**既有埋点清单**（注入即得，无需手写；命名统一后）：

| 传输 | 指标 | 类型 | 维度 |
| --- | --- | --- | --- |
| tcp（server） | `tcp_messages_total` | Counter | status ∈ {ok, error} |
| tcp（server） | `tcp_message_duration_seconds` | Histogram | — |
| tcp（server） | `tcp_connections_in_flight` | GaugeAdd ±1 | — |
| tcp（client） | `tcp_client_messages_sent_total` | Counter | rpc_system |
| tcp（client） | `tcp_client_messages_errors_total` | Counter | rpc_system, error |
| tcp（client） | `tcp_client_message_send_duration_seconds` | Histogram | rpc_system |
| tcp（client） | `tcp_client_messages_received_total` | Counter | rpc_system |
| webrtc（client） | `webrtc_client_messages_sent_total` | Counter | rpc_system |
| webrtc（client） | `webrtc_client_messages_errors_total` | Counter | rpc_system, error |
| webrtc（client） | `webrtc_client_message_send_duration_seconds` | Histogram | rpc_system |
| webrtc（client） | `webrtc_client_messages_received_total` | Counter | rpc_system |
| kafka/rabbitmq/redis/rocketmq | `broker_messages_received_total` | Counter | broker, topic |
| 同上 | `broker_message_duration_seconds` | Histogram | broker, topic |
| 同上 | `broker_messages_errors_total` | Counter | broker, topic, error |

命名已统一为 Prometheus 惯例（下划线 + `_total`/`_seconds` 后缀，
label key 无点号）——任何后端下都可正常抓取/解析。broker 系四个传输
共用一组指标名，`broker` label（kafka/rabbitmq/redis/rocketmq）区分传输。

## 5. 业务自定义埋点

transport 之外，业务代码可直接调三原语（`Metrics` 接口就是为此设计的）：

```go
var m metrics.Metrics = p // 依赖只写契约类型，后端可替换

m.Counter(ctx, "orders_created_total", 1, map[string]string{"channel": "api"})
m.Histogram(ctx, "order_value_dollars", 129.0, map[string]string{"channel": "api"})
m.Gauge(ctx, "queue_depth", 42, map[string]string{"queue": "email"})     // Set：传绝对值
m.GaugeAdd(ctx, "workers_in_flight", 1, map[string]string{"pool": "bg"}) // 增量：±delta
```

三原语语义（三后端行为一致）：

- **Counter**：单调递增计数（次数、字节数）；
- **Histogram**：观测值分布（延迟、大小）；
- **Gauge**：瞬时值（Set 语义，队列深度、缓存条目数）——传绝对值；
- **GaugeAdd**：增量维护（Add 语义，在途数、活跃连接数）——传 ±delta。

命名建议对齐 Prometheus 惯例：小写下划线、Counter 带 `_total`、延迟类带
`_seconds`——这样无论最终选哪个后端都不会被拒。

持有后台资源的后端（otel、datadog）实现 `metrics.Closer`，停机时按需断言：

```go
if c, ok := m.(metrics.Closer); ok {
    _ = c.Close() // flush 尾批
}
```

## 6. 边界与注意事项

- **Gauge 与 GaugeAdd 别用错语义**：`Gauge` 是 Set（传绝对值），
  `GaugeAdd` 是增量（传 ±delta），三后端行为一致。若代码从别的
  指标库迁来、曾以为 `Gauge` 传的是增量，改用 `GaugeAdd`。

- **全局 MeterProvider 单例位互斥**：`metrics/otel.New` 与
  `observability-otlp` 的 `Setup` 都调 `otel.SetMeterProvider`。同时使用
  后设者顶掉先设者，**被顶掉一侧的全部指标静默丢失**。约束：一个进程
  二选一。要同时要 OTLP 直推和 Prometheus 抓取，用 `Setup` 的多 Reader
  （它本来就是为此设计的）。
  **走契约装配时这条由框架兜住**：`instrumentation.otlp` 与 `metrics`
  两段同时出现、且两个段的 Provider 都注册，计划期直接判红（启动、
  `--plan` 预检与热更新重算三条路共用这次拒绝），不会让你靠装配顺序
  赌运气。`instrumentation.prometheus`/`instrumentation.datadog` 不写
  全局位，与 `metrics` 段共存合法。代码里直接调两个构造函数仍然是
  你自己的纪律——框架看不到。
  **此约束只在两个 metrics 后端之间成立**——trace 用的是独立的
  `otel.SetTracerProvider`，与 metrics 的全局 MeterProvider 互不相干，
  可任意组合（`observability-otlp` 本就同时提供 trace 与 metrics）。

- **label keys 首见冻结（D6，已契约化）**：同一指标名第一次出现的
  label key 集合被缓存为维度，之后传更多 label 会被**静默丢弃**而非
  报错——同一指标的 label 集合要在首次调用前定稿（已写入接口契约，
  调用方保证恒定）。

- **接口无错误返回**：埋点是旁路，后端连不上、instrument 创建失败一律
  静默。排障入口是「端点上没数据」这个现象本身——先查后端目标可达性，
  再查指标名/label 是否拼错。

- **两个段别混**：`metrics` 段驱动的是 `pkg/metrics` 那条请求级 Recorder 链
  （它的 Provider 对空/未知 `type` 直接 fail-fast），本体系的埋点后端接的是
  **独立的 `instrumentation` 段**（第 3 节）。在 `metrics` 段里写 `datadog`
  不会有反应——那不是它的取值域。

## 7. FAQ

**Q：业务代码该用 `pkg/metrics` 还是 `metrics/`？** HTTP/gRPC 服务用前者
（中间件自动，配 bconf `metrics` 段即得双通道）；自起消息服务器（TCP/MQ）
用后者（`WithMetrics` 注入，标签自定）。两者可共存于一个进程，但 otel 侧
只能有一个 Provider（第 6 节互斥约束）。

**Q：datadog 后端怎么接？** 两条路：手工 `datadog.New(datadog.WithAddress(...))`
后 `WithMetrics` 注入；或写 `instrumentation.datadog` 段 +
`datadogcontract.AppProvider()`，实例落资源面（第 3 节）。`address` 必填——
留空时库会静默 UDP 到 `127.0.0.1:8125`，agent 不在也读不到同步错误。

**Q：六传输指标名是什么形态？** 已统一为 Prometheus 惯例（
设计文档 D4）：broker 系四传输共用一组 `broker_*` 名 + `broker` label
区分，client 系按传输前缀（`tcp_client_*`/`webrtc_client_*`）。自定义
埋点建议对齐同款惯例（小写下划线、Counter 带 `_total`、延迟带
`_seconds`）。

**Q：注入了 metrics 但端点上没数据？** 按序排查：① 后端目标可达吗
（collector / Agent / `/metrics` 端点挂了吗）；② 指标名/label 拼写一致吗
（label 首见冻结，第 6 节）；③ otel 后端等一个导出周期（缺省 60s）；
④ 是不是被全局 MeterProvider 顶掉了（第 6 节互斥）。
