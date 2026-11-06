// Package metrics 定义 bald 的**传输级**指标埋点契约（Counter/Histogram/Gauge 三原语）。
//
// 面向 TCP 会话、MQ 消费循环、WebRTC 等**没有「请求」概念**的路径：维度天然是
// status/topic/queue 这类自由标签，由埋点方自行命名，后端由业务自选
// （prometheus/otel/datadog，见本 module 的三个子包）。
//
// # 与 pkg/metrics 的区别（同名不同义，易混）
//
// 本仓有**两套并存的指标体系**，二者可共存于一个进程，但职责不同：
//
//	本包（github.com/kalandramo/bald/metrics，传输级）
//	  ├ 手工埋点：业务/传输代码显式调用 Counter/Histogram/Gauge
//	  ├ 维度自由：name + labels 由埋点方决定
//	  └ 后端自选：业务 New() 后注入，或写 bconf `instrumentation.<后端>` 段
//	    由框架装配（段 Provider 在各后端的 contract 子包）
//
//	pkg/metrics（github.com/kalandramo/bald/pkg/metrics，请求级 Recorder）
//	  ├ 中间件自动 emit：HTTP/gRPC 请求经 AuditWithMetrics 自动记录
//	  ├ 维度固定：协议维度（OTel semconv v1.43.0）+ 审计三元组（正交两序列）
//	  └ 契约驱动：bconf metrics 段 → 段 Provider → observability-otlp Setup
//
// 判别规则：**有「请求」的走 pkg/metrics（中间件自动），没有的走本包（手工埋点）。**
// 完整设计与取舍见 docs/devel/zh-CN/Bald 指标设计.md。
//
// # 契约装配（instrumentation 段）
//
// 三个后端各自带一个 contract 子包（`metrics/prometheus/contract`、
// `metrics/otel/contract`、`metrics/datadog/contract`），消费顶层 `instrumentation`
// 段的同名子段：**子段存在即装配**，没有 type 字符串，与 cache/storage/broker 同构。
//
//	instrumentation:
//	  otlp:                      # 存在即装配 metrics/otel
//	    endpoint: "collector:4317" # 必填（留空会静默导出到库默认的 localhost:4317）
//	    insecure: true
//
//产物落资源面，消费侧在域 Wire 里按类型化句柄取回后注入 transport：
//`otelcontract.Otlp.Resource(app)` → `tcp.WithMetrics(m)`。根包与其 contract
//子包的关系和其余域一致：根包保持零契约依赖（纯后端 SDK 实现），只有
//contract 子包 import bconf + appkit。
//
// # 注意事项
//
//   - ⚠️ otel 全局 MeterProvider 单例位：`metrics/otel.New` 与
//     `observability-otlp/metrics.Setup` **互斥**——同时调用时后设者生效、
//     先设者的指标静默丢失。一个进程里二选一。走契约装配时 appkit 在
//     计划期兜住这条纪律：`instrumentation.otlp` 与 `metrics` 两段同时
//     出现即启动失败（`instrumentation.prometheus`/`datadog` 不写全局位，
//     与 `metrics` 段共存合法）；代码里直接调两个构造函数则框架看不到。
//   - label keys 首见冻结：同一 name 的 label key 集合必须跨调用恒定
//     （Prometheus 后端预声明维度，后见的 key 被静默丢弃）。
//
// 示例：
//
//	m, _ := prometheus.New(prometheus.WithNamespace("myapp"))
//	m.Counter(ctx, "tcp_connections_total", 1, map[string]string{"status": "ok"})
//	m.Gauge(ctx, "queue_depth", 42, map[string]string{"queue": "email"})
package metrics
