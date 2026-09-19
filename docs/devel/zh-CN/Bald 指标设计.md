# Bald 指标设计：请求级 Recorder 与传输级三原语

> Title: 指标域统一设计——pkg/metrics Recorder（semconv v1.43.0）+ metrics/ 三原语
>
> Author(s): bald 团队
>
> 适用包：`pkg/metrics`、`pkg/middleware/{gin,grpc}`、`metrics/`、`metrics/{prometheus,otel,datadog}`
>
> 关联文档：[Bald 审计设计](./Bald%20审计设计.md)（同源纪律与旁路纪律）、[Bald 日志设计](./Bald%20日志设计.md)（可观测性闭环）、[Bald Bootstrap 设计](./Bald%20Bootstrap%20设计.md)（契约装配链）、[框架契约总览](./框架契约总览.md)（§9 中间件表）
>
> Last updated: 2026-09-15
>
> Status: Accepted

## 摘要

bald 的指标域是**两套并存的体系**，回答两类不同的问题：

- **请求级 Recorder**（`pkg/metrics`）：HTTP/gRPC 中间件自动 emit，协议指标对齐 OTel semconv v1.43.0（`http.server.request.duration` 等），业务维度走正交的 `bald_audit_events_total`（与审计三元组同源）。
- **传输级三原语**（`metrics/`）：TCP/Kafka/RabbitMQ/Redis/RocketMQ/WebRTC 六个消息服务器的手工埋点契约（Counter/Histogram/Gauge），名字与标签由埋点方决定。

一句话判断：**有「请求」的走 Recorder（中间件自动），没有「请求」的走三原语（手工埋点）**。两套体系可共存于一个进程；仅当传输级选择 otel 后端时，才与请求级 Recorder 互斥抢占 otel 全局 `MeterProvider` 单例位（见「兼容性」）。

## 背景与动机

### 请求级指标：自有命名回答不了协议问题

v0.2.1 原始设计（2026-09-12）里，Recorder 基于审计同源维度 emit 两条自有命名指标——`bald_requests_total`（Counter，transport/object/action/result）与 `bald_request_duration_seconds`（Histogram，transport/object/action）。覆盖 RED 三要素（量/错误率/延迟），命名符合 Prometheus 惯例，但属自有命名空间：套不了社区 dashboard，APM 也不认识。

semconv v1.43.0 对齐（2026-09-15，现行）经三个决策点确认后切换——

1. **正交拆分**（而非把 object/action 叠进 semconv 指标属性）——叠加会撞基数墙：`method(≤9) × status(~30) × route(几十~几百) × object(几十) × action(几个) × result(3)` → 数十万时间序列，超出 Prometheus 单指标数万序列的工程上限。正交后各自数千 / 数百。
2. **gRPC 同切**——HTTP/gRPC 统一对齐，不留双命名空间。
3. **`http.server.active_requests` 补上**——饱和度信号（SRE 四金信号），拦截器已有请求边界，增量小。

切换后：协议问题用协议标准指标回答（可套社区 dashboard / APM 自动识别），业务问题用审计指标回答（M7 同源不丢），两者正交、基数各自可控。

### 传输级指标：没有「请求」的路径塞不进固定维度

TCP 会话与 MQ 消费循环里没有「请求」这个概念，维度天然是 `status`、`topic`、`queue` 这类自由标签，Recorder 的固定维度模型塞不下。`metrics/` 为此提供最小埋点契约（Counter/Histogram/Gauge 三原语）与三个后端实现（prometheus/otel/datadog），服务六个消息服务器——名字与标签由埋点方决定，后端由业务自选。

### 两套体系总览：先读这张表

| | `pkg/metrics`（Recorder） | `metrics/`（Metrics 三原语） |
|---|---|---|
| 定位 | 请求级（HTTP/gRPC） | 传输级（TCP/MQ/WebRTC 消息与会话） |
| 维度 | 协议维度（semconv v1.43.0）+ 审计三元组（正交两序列） | 自由：埋点方传 name + labels |
| 指标 | `http.server.request.duration`、`http.server.active_requests`、`rpc.server.call.duration`、`bald_audit_events_total` | 埋点方命名（tcp/kafka 各自一套） |
| emit 方式 | 中间件自动（`AuditWithMetrics`） | 业务/传输代码手工调用 |
| 后端装配 | bconf `metrics` 段 → appkit `MetricsRegistry` → `observability-otlp/contract` → `Setup` 多 Reader | 业务自选后端 `New()` 直连，**不经契约** |
| otel 依赖 | 契约层即 otel API（全局 MeterProvider） | 契约层零依赖，otel 只是三后端之一 |
| panic 防护 | `emitMetricsSafely` recover 降级 | 无（tcp 直接调用；当前三后端不主动 panic，风险低） |
| 发版 | 随根模块 | **零 tag**（见「实现与过渡」） |

**场景判断规则：**

- **HTTP/gRPC 服务** → 用 `pkg/metrics`：中间件自动 emit，配 bconf `metrics` 段即得双通道。协议指标可套社区 dashboard、被 APM 自动识别；业务维度与审计三元组同源——两个视角正交，基数各自可控。
- **自起 TCP 会话 / MQ 消费循环** → 用 `metrics/`：这些路径里没有「请求」，维度天然是 `status`、`topic`、`queue` 这类自由标签，Recorder 的固定维度塞不下。
- **一个进程两者都有**（既起 HTTP 又起 MQ 消费）→ 两套并存可以，但 **otel 侧只能有一个 Provider**（见「兼容性」）。

## 设计

### 请求级 Recorder（pkg/metrics）

#### 指标清单（semconv v1.43.0）

| 指标 | 类型 | 单位 | 属性 | 回答的问题 |
|---|---|---|---|---|
| `http.server.request.duration` | Histogram | s | http.request.method、url.scheme、url.template、http.response.status_code | 这个路由慢不慢、错多少（协议视角） |
| `http.server.active_requests` | UpDownCounter | {request} | url.scheme、server.address、server.port | 当前在途请求数（饱和度） |
| `rpc.server.call.duration` | Histogram | s | rpc.system.name="grpc"、rpc.method、rpc.response.status_code | 这个 RPC 方法慢不慢、错多少 |
| `bald_audit_events_total` | Counter | 1 | transport、object、action、result | 哪类业务操作被拒/出错多少（业务视角，M7 同源） |

被替换：`bald_requests_total`、`bald_request_duration_seconds`（随根模块 v0.6.1 发布过，属破坏性变更，见「兼容性」）。

**semconv v1.43.0 现行事实**（与旧版认知的差异，以代码库内 `go.opentelemetry.io/otel@v1.46.0/semconv/v1.43.0` 生成物为准）：

| | 旧认知（已废弃） | v1.43.0 现行 |
|---|---|---|
| gRPC 指标名 | `rpc.server.duration` | **`rpc.server.call.duration`** |
| gRPC 单位 | ms（历史遗留坑） | **s**（坑已修，与 HTTP 统一） |
| gRPC 状态属性 | `rpc.grpc.status_code`（int） | **`rpc.response.status_code`**（string，驼峰如 `"PermissionDenied"`，codes.Code.String()） |
| gRPC 服务维度 | `rpc.service` | 无此属性——`rpc.method` 直接收 fully-qualified 名 |
| HTTP 路由属性 | `http.route` / `url.route` | **`url.template`**（httpconv 生成物现行） |

HTTP 侧现行定义（`httpconv/metric.go`）：`http.server.request.duration` 必选属性 `http.request.method` + `url.scheme`，可选 `url.template`、`http.response.status_code`、`server.address`、`server.port`、`error.type`；`http.server.active_requests` 属性 `server.address` + `server.port`（+ `url.scheme`）。

gRPC 侧现行定义（`rpcconv/metric.go`）：`rpc.server.call.duration` 默认桶 0.005~10s，必选属性 `rpc.system.name`（gRPC 取 `"grpc"`），可选 `rpc.method`、`rpc.response.status_code`、`server.address`、`error.type`。

#### Recorder 接口与 Event

```go
type Transport string
const (
    TransportGRPC Transport = "grpc"
    TransportHTTP Transport = "http"
)

// Recorder 指标记录器；具体 instrument 由核心用 otel 构建，默认 noop。
type Recorder interface {
    Record(ctx context.Context, ev Event, transport Transport, durationSeconds float64)
    // RecordActive 维护在途请求数（http.server.active_requests）。
    // ev 只需填 Request 部分（请求开始时 result/object 尚未产生）。
    // semconv 仅定义 HTTP 侧；gRPC 侧调用为 no-op。
    RecordActive(ctx context.Context, ev Event, transport Transport, delta int64)
}

// Event 是喂给指标的精简事件视图（避免 metrics 包反向依赖 audit 包）。
type Event struct {
    Object string
    Action string
    Result string // allow / deny / error
    Error  string

    // Request 是协议维度（semconv v1.43.0 对齐），由拦截器从请求上下文提取。
    // 零值兼容：不填则协议指标缺对应属性（不报错）。
    Request RequestInfo
}

// RequestInfo 是协议维度视图：HTTP 与 gRPC 共用一字段集，按 transport 取用。
type RequestInfo struct {
    Method     string // HTTP: "GET"；gRPC: FullMethod（rpc.method 收 fully-qualified 名）
    Scheme     string // HTTP: "http"/"https"；gRPC: 空
    Template   string // HTTP: 路由模板（"/v1/secret/:id"，url.template）；gRPC: 空
    StatusCode int    // HTTP: 200/403…；gRPC: codes.Code 数值（emit 时转 string）
    ServerAddr string // HTTP: Host 的 host 部分；gRPC: 空
    ServerPort int    // HTTP: Host 的 port 部分（0 = 未知，省略属性）；gRPC: 空
}
```

构造：

```go
func New(meterName string) Recorder  // 用全局 MeterProvider 构建（no-op provider 时自动退化 no-op）
func NopRecorder() Recorder          // 静默默认
```

`meterName` 通常为 `"bald/<transport>"`。若调用方未配置全局 `MeterProvider`，otel 的 `MeterProvider()` 返回 no-op，instrument 退化为 no-op，`Record` 不产生副作用——零配置可跑。

otelRecorder 持四 instrument（`httpDuration` / `rpcDuration` / `activeReqs` / `auditEvents`），`Record` 按 transport 分流：HTTP 记 `httpDuration`（method/scheme 必带，template/status 非零补），gRPC 记 `rpcDuration`（system.name="grpc" 必带，method/status 补）；两侧统一再 `auditEvents.Add(1, transport/object/action/result)`。gRPC 的 `rpc.response.status_code` 用 `codes.Code(n).String()`（semconv 定义为 string）。

#### 拦截器协议维度提取

**gin**（`AuditMiddleware`）：请求开始构造 `RequestInfo`（method=`c.Request.Method`、scheme= TLS 判断、template=`c.FullPath()`、server=`c.Request.Host` 拆分），`RecordActive(+1)` + defer `RecordActive(-1)`；`c.Next()` 后补 `StatusCode=c.Writer.Status()` 再 `Record`。gin 的 Use 中间件在路由匹配后执行，`FullPath()` 非空（404 走 NoRoute 不经此链）。

**gRPC**（`AuditInterceptor`）：handler 返回后构造 `RequestInfo{Method: info.FullMethod, StatusCode: int(status.Code(err))}`；不调 `RecordActive`（semconv 无 RPC 在途数定义）。

记录时机与审计同源：拦截器在请求结束时一次填充 `(object, action, result, duration)`，同步 emit——一次中间件记录，既进审计又出指标。结果分类与 `audit.Result` 对齐（allow/deny/error），错误率即 `bald_audit_events_total{result="error"}` 的计数占比。

#### 自动装配路径（2026-09-12，bald v0.2.1）

手动接线（业务 main.go 自调 `obmetrics.Setup` 翻译契约字段）之外，契约 `metrics` 段可经 FromBootstrap 自动装配：

```go
mr := appkit.NewMetricsRegistry()
mr.MustRegister("prometheus", otlpcontract.NewPrometheusProvider(appName))
mr.MustRegister("otlp", otlpcontract.NewOTLPProvider(appName))
// 顶层只需：
appkit.FromBootstrap(bootstrap, appkit.WithMetricsRegistry(mr), ...)
```

- `metrics.type` 单选查表（prometheus=仅本地抓取 / otlp=抓取+直推双通道），段缺省 no-op、type 空/未注册 fail-fast——与 `bootstrap.RegistrarRegistry` 同语义。
- 暴露端独立于业务 server（`appkit.StartMetricsServer`，缺省 `:9091` `/metrics`），生命周期解耦：业务 server drain 时指标端仍在产出尾批数据。
- 停机 flush Effect 逆序回放**最后**执行（所有组件 cleanup 后）。
- serviceName 即时绑定：contract Provider 构造器收 `serviceName string`（如 `NewPrometheusProvider(appName)`），构造时求值——resource 的 `service.name` 来自应用而非契约段，故导出构造器而非包级 Provider 变量。

#### 惰性 instruments（noop-bind 修复史，v0.2.1）

otelRecorder 曾在构造期创建 instruments——OTel global 语义下，`SetMeterProvider` **之前**创建的 instrument 永久 no-op（绑定时捕获的是旧 noop meter）。Recorder 常在 Provider 装配前构造（如 bundle 构造期），症状是「指标端可见但恒空、无错误」。修复：instruments 改 `sync.Once` 首次 `Record` 时惰性创建（v0.2.1）。此前 go-bald-admin「不压流量恒空」的部分误判根因即此。

### 传输级三原语（metrics/）

> 本节行为以代码为准，已知缺陷及修复状态见「实现与过渡」。

#### 契约：三原语接口，模块零依赖

`metrics/metrics.go` 全部内容就是两个接口：

```go
type Metrics interface {
    // Counter 单调递增计数。
    Counter(ctx context.Context, name string, value float64, labels map[string]string)
    // Histogram 观测值分布（延迟、大小）。
    Histogram(ctx context.Context, name string, value float64, labels map[string]string)
    // Gauge 设置瞬时值（Set 语义：value 是新的绝对读数，非增量）。
    // 增量式用法（如在途数）用 GaugeAdd。
    Gauge(ctx context.Context, name string, value float64, labels map[string]string)
    // GaugeAdd 增量累加（Add 语义）。
    GaugeAdd(ctx context.Context, name string, delta float64, labels map[string]string)
}

type Closer interface {
    Close() error
}
```

五点边界（前四点以代码为准，第五点为埋点纪律）：

- **无返回值**：埋点永不向上游报错，失败静默（旁路纪律，与 audit.D5 同源）。代价是「可观测性自身不可观测」——后端挂了你只能在指标端点上发现数据没了。
- **`ctx` 由后端自行处置**：otel 后端把 ctx 传给 `Add`/`Record`（参与 OTel 上下文传播）；prometheus 与 datadog 后端显式 `_ = ctx` 丢弃。
- **`Closer` 是能力探测接口而非生命周期要求**：持有后台资源（OTLP 缓冲、UDP 连接）的后端实现它（otel、datadog 有 `var _ metrics.Closer` 断言），prometheus 无需 Close。调用方用类型断言决定是否挂停机。
- **label keys 须恒定**：同一 name 的 label key 集合跨调用保持不变——prometheus 后端首见冻结维度，后见新键静默丢弃（接口无返回值，无法报错）。
- **label values 须低基数**：`error` 这类维度应取错误类别（如 `timeout` / `unavailable` / `connection_refused`）而非原始错误消息——自由文本会撑爆时间序列基数，且后端无上限保护、静默丢弃。

`metrics/go.mod` 只有 module 声明与 `go 1.27.1`，是全仓最纯的契约模块——比 berrors 根包（零第三方依赖但属根模块）更进一步：整个模块可被任何后端零成本引入。

#### 模块布局：一个契约，三个后端

```
metrics/                  module github.com/kalandramo/bald/metrics          （零 require）
├── prometheus/           module …/metrics/prometheus  → client_golang v1.23.2
├── otel/                 module …/metrics/otel        → otel v1.46.0 全家（grpc/http exporter、sdk/metric）
└── datadog/              module …/metrics/datadog     → DataDog/datadog-go/v5 v5.8.2
```

三个后端统一形态：`New(opts ...Option) (*Provider, error)` + `var _ metrics.Metrics = (*Provider)(nil)` 断言 + functional options。命名对齐六域桥接模块的顶层布局判别（独立 go.mod = 可独立发布），但这四个模块**都还没有发过版**。

#### Prometheus 后端：promauto 工厂 + 七个惰性缓存 map

`Provider` 持有 cfg、`promauto.Factory` 与七个 map（counters/counterVecs/histograms/histogramVecs/gauges/gaugeVecs/labelNames），全部由一把 `sync.Mutex` 保护（2026-09-15 D1 修复——此前无锁，tcp 并发 handler 下必现 data race；锁内查建 instrument、锁外执行 Add/Observe/Set，instrument 自身并发安全）。instrument 首次使用时创建并缓存；无标签走单 instrument，有标签走 `*Vec`。

有两个构造器，行为差异是刚性的：

- `New()`：缺省私有注册表（`prometheus.NewRegistry()`），互相隔离；`Registry()` 返回它供 `promhttp.HandlerFor` 挂自有端点。`WithRegistry` 可覆盖（2026-09-15 D2 修复——此前 `New()` 无条件覆盖丢弃，Option 失效）。
- `NewWithDefaultRegistry()`：缺省 `prometheus.DefaultRegisterer`，指标混入进程全局注册表，`promhttp.Handler()` 可直接暴露。`WithRegistry` 同样可覆盖。

已知边界：**label keys 首见冻结**（`cachedLabelKeys`）。同一 name 第一次出现的 label key 集合被缓存为该指标的 Vec 维度，之后传入不同（更多）label 的键会被静默丢弃而非报错——这是 Prometheus 同名指标维度必须固定的本质约束，已写入接口契约（调用方保证 label keys 恒定）。

#### OTel 后端：OTLP 直推 + 全局 MeterProvider

`New()` 组装链：OTLP exporter（gRPC 缺省 / `WithHTTP` 切换）→ resource（semconv v1.43.0 的 ServiceName/Version，2026-09-15 自 v1.34.0 升级统一）→ PeriodicReader（缺省 60s）→ `MeterProvider` → **`otel.SetMeterProvider(mp)` 设为全局**。四 map（counters/histograms/gauges/gaugeAdds）有 `sync.Mutex` 保护，instrument 创建失败静默 return。`Close()` 以 10s 超时 `Shutdown` flush 尾批。

**Gauge 用同步 `Float64Gauge` instrument**（2026-09-15 D3 修复）：OTel Go v1.46 提供同步 gauge，`Record` 即真 Set 语义，不再需要 observable 回调或 UpDownCounter 模拟。`GaugeAdd` 走 `Float64UpDownCounter.Add`（增量语义）。同名混用两种语义时第二个 instrument 创建失败、静默跳过（旁路纪律）。

缺省 `serviceName = "bald-app"`（`otel.go:56`；2026-09-15 自 go-wind 移植残留 `"go-wind-service"` 清理，对齐 `observability-otlp` 的 `defaultServiceName` 惯例）。

#### Datadog 后端：DogStatsD UDP

最薄的一层：`statsd.Client` 直连 `127.0.0.1:8125`，labels 转 `k:v` tag，`rate` 支持采样。`WithFlushPeriod` 经 `statsd.WithAggregationInterval` 消费（2026-09-15 D5 修复——此前是死配置）；`Counter`/`GaugeAdd` 的浮点增量经 `math.Round` 转整（D7 修复——此前直接截断）；`GaugeAdd` 用 statsd count 类型（增量语义），`Gauge` 用 statsd gauge（Set 语义）。

#### 消费方式与既有埋点清单

六个传输模块经 `WithMetrics(m metrics.Metrics)` 注入（与 `WithTracer` 对称；webrtc 客户端侧是 `WithClientMetrics`）。已落地的埋点（实测代码，2026-09-15 命名统一后）：

| 传输 | 指标 | 类型 | 维度 |
|---|---|---|---|
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

命名已统一为 Prometheus 惯例（2026-09-15 D4 修复）：小写下划线、Counter 带 `_total`、延迟带 `_seconds`、label key 不含点号（`rpc.system`→`rpc_system`）。broker 系四个传输共用一组指标名，`broker` label（取值 kafka/rabbitmq/redis/rocketmq）区分传输。

#### 与契约装配链的关系：不经 bconf，datadog 无契约位

bconf 的 `metrics` 段（`type ∈ {"prometheus", "otlp"}`）驱动的是 `pkg/metrics` 那条链（appkit `MetricsRegistry` → `observability-otlp/contract` → `Setup`），**与本接口无关**。本体系的三个后端只能业务手工 `New()` 后注入 transport；datadog 在契约里没有取值，永远走不了配置驱动。

## 理由与取舍

### 两套体系为什么并存，而不是合并成一套

合并的两个方向我们都否决过。向上合并（Recorder 泛化为自由标签）会丢掉「与审计三元组同源」的结构保证——维度一自由，`(secret, get)` 在指标里就可能变成 `(secrets, read)`，按指标排查对不上审计详情（M7 的教训）。向下合并（Metrics 收敛为固定维度）则消息服务器的 topic/queue/status 全部塞不进 object/action 模型。重叠的代价我们如实承认：双全局 MeterProvider 抢位、双套命名空间、后端配置两套——这是「全局单例位互斥」约束的根源，长期看值得收敛为「共享 Provider、两套语义层」，但那是独立提案，不混入现状记录。

### 请求级 Recorder 的取舍

**决策①：为什么 `Event` 不直接复用 `audit.AuditEvent`？** 避免 `pkg/metrics` 反向依赖 `pkg/audit`——两个抽象应平行存在（都可独立用于不同拦截器组合）。拦截器负责从审计事件拷贝出精简字段，成本是一次小结构赋值。

**决策②：为什么基于 otel metric 而非自研 instrument？** 指标是 Go 生态最成熟的标准（otel 全局 API），bald 只做"语义维度固化"（命名/维度/单位），不做计量引擎。未配置 provider 自动 no-op 使核心保持零耦合可选——与 `log`/`audit` 的"零后端可跑"纪律一致。

**决策③：为什么审计维度与 semconv 协议维度正交，而非叠加？** 叠加的基数爆炸见「背景与动机」。正交的代价：跨视角查询要经 `url.template ↔ object` 关联（两指标各自查）——接受，因为「按路由看性能」与「按业务看审计」本来就是两个问题、两拨消费者。审计三元组同源的保证不丢：同一请求在审计是 `(secret, get)`，在 `bald_audit_events_total` 就是 `(secret, get)`——按指标排查时能对应审计详情（M7 的教训）。

**决策④：`bald_audit_events_total` 与 semconv 指标同批落地（原计划两阶段）。** 原评估拟「审计重构时再补」，实现时发现 `Event` 三元组数据源在切换后依然完整（拦截器本来就构造它），一次落地消除了「按业务维度看指标」的断裂期——阶段二提前，无额外成本。

**决策⑤：属性集取 semconv 必选 + 低基数可选，不追全量。** `error.type`、`network.protocol.version` 等可选属性不采（增量基数、拦截器提取成本）；`server.address/port` 在 active_requests 上尽力提供（Host 拆分，缺失省略）——gin 中间件层拿不到可靠 listener 地址，这是诚实边界而非静默偷工。

**决策⑥：gRPC 状态码用 string 语义。** semconv v1.43.0 的 `rpc.response.status_code` 是 string，取 `codes.Code(n).String()`（驼峰 `"OK"`/`"PermissionDenied"`——与 otelgrpc 惯例一致），不是旧版 int。与 HTTP 的 int `http.response.status_code` 类型不同——消费侧查询语法注意。

**决策⑦：指标名直接用 semconv 原名（点分隔），不预转 Prometheus 形态。** OTLP 导出后 Prometheus 端自动转 `http_server_request_duration_seconds`（加 `_seconds` 后缀、点转下划线）——预转反而破坏 OTel 原生消费链（如直推 Datadog 的 OTLP intake）。

### 传输级三原语的取舍

**决策①：接口为什么无错误返回？** 埋点是旁路：后端连不上 Collector、UDP 丢包、instrument 创建失败，都不该影响消息处理主路径。这与 `audit.Auditor` 的 D5 纪律对称。代价是静默丢数，且没有自身的健康信号——我们接受，因为指标链路的排障入口是「端点上没数据」这个现象本身。

**决策②：Gauge 与 GaugeAdd 为什么是两个方法？** OTel 的 gauge 历史上只有 observable（回调注册、周期采集）形态，命令式接口接不进——旧实现因此退化为 `Float64UpDownCounter.Add`，与 prometheus/datadog 的 Set 语义分歧（换后端 = 换行为）。OTel Go v1.46 提供同步 gauge 后我们彻底拆开：`Gauge` = Set（otel 用 `Float64Gauge.Record`，prometheus 用 `Gauge.Set`，datadog 用 statsd gauge），`GaugeAdd` = 增量（otel 用 `Float64UpDownCounter.Add`，prometheus 用 `Gauge.Add`，datadog 用 statsd count）。三后端两种语义各自一致，tcp 的在途数 ±1 用法改走 `GaugeAdd`。

**决策③：后端为什么放顶层子模块而不是 contrib/？** 对齐布局判别：独立 go.mod = 可独立发布的桥接模块，且契约（零依赖）与后端（重依赖）必须物理隔离，否则 transport 六模块会被迫传递引入 client_golang/otel/datadog-go。contrib/ 语义是「接根模块运行期框架」，这三者不接任何框架，只是 SDK 桥接。2026-09-17 该判别再落一例：服务注册（registry 契约 + `registry/{etcd,consul,nacos,kubernetes}` 后端）同样按此从 `pkg/registry` + `contrib/registry` 归位到顶层独立 module，见《Bald 注册中心设计.md》。

**决策④：为什么 `New()` 与 `NewWithDefaultRegistry()` 是两个函数？** 私有注册表隔离（`New`）与全局注册表混用（`NewWithDefaultRegistry`）是两种合法策略，用两个构造器区分是清晰的。曾经的矛盾（`New()` 无条件覆盖 `WithRegistry`，三者互相打架）已于 2026-09-15 修复（D2）：`WithRegistry` 对两个构造器都生效，缺省值各自兜底。

## 兼容性

### 请求级 Recorder：三项破坏性变更，装配链不变

**破坏性变更**（随下个版本发布，先例 9454bec log 收缩）：

| 变更 | 影响 | 迁移 |
|---|---|---|
| 指标名：`bald_requests_total` / `bald_request_duration_seconds` 移除 | Grafana 面板/告警规则 | 请求量改查 `bald_audit_events_total`（按 transport 聚合）；HTTP 面板改查 `http_server_request_duration_seconds`（Prometheus 形态）；错误率改 `bald_audit_events_total{result="error"}` 或按 status_code 聚合 |
| `Recorder` 接口加 `RecordActive` 方法 | 自定义 Recorder 实现 | 补一个空方法（或转发 `NopRecorder`） |
| `Event` 加 `Request` 字段 | 构造 `Event` 的调用方 | 零值兼容，不填则协议指标缺属性（不报错） |

**不变项**：`Record` 签名、`Transport` 枚举、`NopRecorder`、`New(meterName)` 构造；bconf `metrics` 段 → appkit `MetricsRegistry` → `observability-otlp` 装配链全程无感（Recorder 内部 emit 什么变了，链路没变）；`metrics/`（传输级）体系不受影响——TCP/MQ 埋点的 semconv（`messaging.*`）是独立讨论。

**已知边界**：

- `url.scheme` 判定只看 `c.Request.TLS`——经反代/网关终止 TLS 的场景 scheme 记为 `http`（除非业务自行设 `X-Forwarded-Proto` 处理，本框架不猜）。
- gRPC stream 调用不经 `AuditInterceptor`（unary only，存量边界不变）。
- `http.server.active_requests` 的 `server.address` 取 `Host` 头——可被客户端伪造，仅作 semconv 合规尽力，不用于安全判定。

### 传输级三原语：零 tag、Gauge 语义分歧、单例位互斥

**零 tag：事实上没有外部用户，但 transport 欠着编造版本。** 四个模块从未发版，接口可以自由修正而不破坏任何人。但 `transport/tcp`、`transport/webrtc` 的 go.mod require `metrics v0.0.1`——**该 tag 不存在**；`transport/{kafka,rabbitmq,redis,rocketmq}` require 伪版本 `v0.0.0-00010101000000-000000000000` 靠 replace 兜底。当前全靠 `replace ../../metrics` 本地开发在编译；一旦去掉 replace 发布，tcp/webrtc 必然解析失败。

**Gauge 语义：接口加 `GaugeAdd` 方法（破坏性，零 tag 窗口内自由改）。** 修复前接口只有 `Gauge`（Set 契约），但 otel 后端实际是 Add（`Float64UpDownCounter` 模拟）、tcp 埋点又按增量用法调用——两个相反方向的偏离恰好只在 otel 组合下互相抵消，同一份业务代码从 otel 切到 prometheus 后端，in_flight 指标就静默坏掉。修复（2026-09-15）：`Gauge` 定死 Set 语义（otel 改用同步 `Float64Gauge.Record`），新增 `GaugeAdd` 承载增量语义，三后端行为一致；tcp 的 `connections_in_flight` ±1 改走 `GaugeAdd`。自定义 `Metrics` 实现需补一个 `GaugeAdd` 方法。

**全局 MeterProvider 单例位：与 observability-otlp 互斥使用。** `metrics/otel.New` 与 `observability-otlp/metrics.Setup` 都调 `otel.SetMeterProvider`。同时使用时后设者生效、先设者的全部指标静默丢失；`pkg/metrics` 的 Recorder 依赖全局位，被 `metrics/otel` 顶掉后请求级指标也会路由错误。约束：**一个进程里二选一**。要同时要 OTLP 直推和 Prometheus 抓取，用 `Setup` 的多 Reader（它本来就是为此设计的）。

**这条约束只在 metrics 内部成立——trace 不受影响，可与 metrics 任意组合。** OTel 的 `MeterProvider` 与 `TracerProvider` 是**两个互相独立的全局单例**（`otel.SetMeterProvider` 与 `otel.SetTracerProvider` 分属不同 API）。全仓的全局设置点证实这一点：

| 设置点 | API | 归属 |
|---|---|---|
| `metrics/otel/otel.go` | `SetMeterProvider` | metrics |
| `observability-otlp/metrics/metrics.go` | `SetMeterProvider` | metrics |
| `observability-otlp/trace/trace.go` | `SetTracerProvider` | **trace** |

没有任何 trace 代码调 `SetMeterProvider`，也没有任何 metrics 代码调 `SetTracerProvider`。所以「tracer 与 metrics 都走 OTLP 直推」**完全可行**——`observability-otlp` 这个 module 同时提供 `trace/` 与 `metrics/`，其 `contract` 子包的用法示例（`TracerRegistry` + `MetricsRegistry` 同时注册）就是为此设计的。互斥只发生在「`metrics/otel` 与 `observability-otlp/metrics` 两个 metrics 后端之间」，与 trace 无关。

## 实现与过渡

### 缺陷清单（按严重度）

| # | 缺陷 | 位置 | 严重度 |
|---|---|---|---|
| D1 | prometheus Provider 七 map **无锁**，并发埋点 data race（otel 版有 mu，datadog 版无 map 所以幸免；tcp 的 handler 是并发的，prometheus 后端下必现） | `prometheus.go` 全文件 | 高，已修复（2026-09-15：单 `sync.Mutex` 保护全部缓存 map，`-race` 并发测试覆盖） |
| D2 | `New()` 无条件覆盖 `cfg.registry`，`WithRegistry` 失效 | `prometheus.go:81-82` | 高，已修复（2026-09-15：defaultConfig registry 改 nil、两构造器各自兜底，`WithRegistry` 对两者生效） |
| D3 | Gauge Set/Add 语义跨后端分歧（见「兼容性」）；tcp 增量用法仅 otel 正确 | 接口 + 三后端 + tcp | 高，已修复（2026-09-15：`Gauge` 定 Set + 新增 `GaugeAdd`，otel 用同步 `Float64Gauge`，tcp 改 `GaugeAdd`） |
| D4 | 点号命名不符合 Prometheus 指标名字符集，prometheus 后端下不可抓 | transport 六传输（实际范围大于初记：broker 系 4 server + tcp/webrtc client + `rpc.system` label key） | 中，已修复（2026-09-15：全量统一 Prometheus 命名，见埋点清单） |
| D5 | `WithFlushPeriod` 声明未消费 | `datadog.go:68-71` | 低，已修复（2026-09-15：经 `statsd.WithAggregationInterval` 消费） |
| D6 | label keys 首见冻结，后见 label 静默丢弃 | `prometheus.go:144-151` | 低，已契约化（2026-09-15：写入接口注释，调用方保证同一 name 的 label keys 恒定） |
| D7 | datadog `Counter` int64 截断 | `datadog.go:125` | 低，已修复（2026-09-15：`math.Round` 替代截断） |
| D8 | otel 缺省 serviceName `"go-wind-service"` 血统残留；`doc.go` "go-wind framework" 措辞过期 | `otel.go:56`、`doc.go:2` | 低，已修复（2026-09-15，缺省改 `bald-app`） |

处置原则：D1-D8 已全部于 2026-09-15 修复（D6 契约化——prometheus 同名指标维度固定是本质约束，接口无返回值无法报错，写入契约由调用方保证）。三后端测试从零补齐：prometheus 含 `-race` 并发用例与 Set/Add 语义断言，otel 用 ManualReader 断言 Set/Add 语义，datadog 用本地 UDP 收包断言 DogStatsD 线格式。

### 发版 checklist

1. ~~修 D1-D3~~（已完成，2026-09-15：D1 单锁 + `-race` 测试；D2 defaultConfig nil + 两构造器兜底；D3 Gauge=Set + `GaugeAdd` 正名，otel 同步 gauge，tcp 改 `GaugeAdd`；D4-D7 一并清理）。
2. 清理 `doc.go` / 缺省 serviceName 的 go-wind 残留（已完成，2026-09-15：doc.go 改 "bald framework"、缺省 serviceName 改 `bald-app`）。
3. 打 tag：`metrics/v0.1.0` + `metrics/{prometheus,otel,datadog}/v0.1.0`（lightweight，对齐仓库惯例）。
4. transport 六模块 require 改真实版本，replace 保留（发版随迁惯例；新 tag 撞 sumdb 收录延迟用 `GONOSUMDB` 绕过）。

## 附录：FAQ

**Q：业务代码该用 `pkg/metrics` 还是 `metrics/`？** HTTP/gRPC 服务用前者（中间件自动，配 bconf `metrics` 段即得双通道）；自起消息服务器（TCP/MQ）用后者（`WithMetrics` 注入，标签自定）。两者可以共存于一个进程，但 otel 侧只能有一个 Provider（见「兼容性」）。

**Q：datadog 后端怎么接？** 只能手工：`datadog.New(datadog.WithAddress(...))` 后 `WithMetrics` 注入。契约段无 `datadog` 取值，配置驱动走不通——若需要，是 bconf `Metrics.Type` 枚举的独立增量提案。

**Q：为什么六传输指标名是这个形态？** `metrics.Metrics` 对命名零约定，各传输移植时曾各自带原生态惯例（tcp 下划线、kafka 点号），2026-09-15 已统一为 Prometheus 惯例（D4）：broker 系四传输共用一组 `broker_*` 名 + `broker` label 区分，client 系按传输前缀（`tcp_client_*`/`webrtc_client_*`）。

**Q：Grafana 面板从旧指标名怎么迁？** 见「兼容性」一节的迁移表——`bald_requests_total` 改 `bald_audit_events_total`，`bald_request_duration_seconds` 改 `http_server_request_duration_seconds`（Prometheus 形态）。
