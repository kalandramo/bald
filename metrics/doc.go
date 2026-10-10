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
//	  └ 后端自选：业务 New() 后注入，**不经 bconf 契约**
//
//	pkg/metrics（github.com/kalandramo/bald/pkg/metrics，请求级 Recorder）
//	  ├ 中间件自动 emit：HTTP/gRPC 请求经 AuditWithMetrics 自动记录
//	  ├ 维度固定：协议维度（OTel semconv v1.43.0）+ 审计三元组（正交两序列）
//	  └ 契约驱动：bconf metrics 段 → appkit MetricsRegistry → observability-otlp
//
// 判别规则：**有「请求」的走 pkg/metrics（中间件自动），没有的走本包（手工埋点）。**
// 完整设计与取舍见 docs/devel/zh-CN/Bald 指标设计.md。
//
// # 注意事项
//
//   - ⚠️ otel 全局 MeterProvider 单例位：`metrics/otel.New` 与
//     `observability-otlp/metrics.Setup` **互斥**——同时调用时后设者生效、
//     先设者的指标静默丢失。一个进程里二选一。
//   - label keys 首见冻结：同一 name 的 label key 集合必须跨调用恒定
//     （Prometheus 后端预声明维度，后见的 key 被静默丢弃）。
//
// 示例：
//
//	m, _ := prometheus.New(prometheus.WithNamespace("myapp"))
//	m.Counter(ctx, "tcp_connections_total", 1, map[string]string{"status": "ok"})
//	m.Gauge(ctx, "queue_depth", 42, map[string]string{"queue": "email"})
package metrics
