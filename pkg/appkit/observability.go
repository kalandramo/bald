// observability.go 提供可观测性后端（tracer / metrics 契约段）的装配接缝类型。
//
// tracer/metrics 段经**段驱动 Provider** 装配：后端实现方
// （如 contrib/observability-otlp）导出的构造函数返回 TracerProvider /
// MetricsProvider 函数值，用户以 Provider 消费 tracer/metrics 段并在其
// Build 内按契约 type 单选分发（契约语义就是单选：tracer.type ∈ {"otlp"}，
// metrics.type ∈ {"prometheus","otlp"}）。
//
// metrics 暴露端的**监听面归能力包**：公开包 observability/metricshttp 持有
// Start/Stop（App 层不自建 http.Server）。MetricsProvider 契约是「返回可
// 关闭的暴露端（MetricsExposure）」：后端
// 的 Build 内起监听（metricshttp.Start），把端点句柄交回引擎；引擎把停机
// 合成进 canonical 槽位——先停暴露端、再 flush（storeProduct 的 metrics
// 分支），尾批指标不因监听面先收而丢失。
//
// 生命周期约定（用户 Provider 遵守）：阶段 B（BeforeStart，契约装载校验后）
// Build——必须晚于配置装载（值来自契约），早于 servers 监听（BeforeStart
// 语义保证），使 Recorder / tracer 中间件在首个请求前接入全局 Provider；
// cleanup 交引擎挂停机 Effect（canonical 序：metrics-server、tracer-shutdown
// 的回放位次与旧 12 段流一致）。两段均不支持热更新。
//
// 段缺省语义（契约级，框架保守缺省）：
//   - tracer 段缺省 → no-op trace（otel 全局默认，零配置可运行）；
//   - metrics 段缺省 → 不装配（无暴露端点）。
//   - 段存在而无 Provider → L3 覆盖检查启动期报错（原「Registry 未接线」
//     fail-fast 的等价物，见 segment_coverage.go）。
package appkit

import (
	"context"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// TracerProvider 按契约 Tracer 段装配全局 trace 后端。
// 返回 shutdown（flush 缓冲 span，进程退出前调用），由引擎挂停机 Effect
// 逆序回放（最后执行，收集全部尾批 span）。未开启远端的实现返回 no-op
// shutdown。
type TracerProvider func(ctx context.Context, cfg *bootstrapv1.Tracer) (func(context.Context) error, error)

// MetricsExposure 是 metrics 暴露端（Prometheus 抓取独立监听面）的可关闭
// 句柄——MetricsProvider 产物契约。实现由能力包
// observability/metricshttp 提供（metricshttp.Start 返回的 *Server 天然满足）；
// 引擎保证停机序「先 Stop 暴露端、再执行 MetricsProvider 的 flush」。
type MetricsExposure interface {
	// Stop 停止暴露端：关闭监听、排空在途请求。不 flush 指标——flush 归
	// MetricsProvider 返回的 shutdown（停机序由引擎合成，见 storeProduct）。
	Stop(ctx context.Context) error
}

// MetricsProvider 按契约 Metrics 段装配指标后端。
// 返回 (exposure, flush)：exposure 是 Prometheus 抓取暴露端（实现自起独立
// 监听，推荐经 observability/metricshttp.Start；无暴露面的实现返回 nil）；
// flush 关闭 OTLP 直推通道并释放 MeterProvider（进程退出前调用，引擎保证
// 其在 Stop 暴露端之后执行）。
type MetricsProvider func(ctx context.Context, cfg *bootstrapv1.Metrics) (MetricsExposure, func(context.Context) error, error)
