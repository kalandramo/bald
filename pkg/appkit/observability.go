// observability.go 提供可观测性后端（tracer / metrics 契约段）的装配基座。
//
// 批 2 W2b 起，tracer/metrics 段经**段驱动 Provider** 装配：后端实现方
// （如 contrib/observability-otlp）导出的构造函数返回 TracerProvider /
// MetricsProvider 函数值，用户以 Provider 消费 tracer/metrics 段并在其
// Build 内按契约 type 单选分发（契约语义就是单选：tracer.type ∈ {"otlp"}，
// metrics.type ∈ {"prometheus","otlp"}）。本文件保留两件事：
//   - TracerProvider / MetricsProvider 函数契约（后端与用户 Provider 的
//     接缝类型，contrib/observability-otlp/contract 按此构造）；
//   - StartMetricsServer（metrics 暴露端独立监听面的生命周期工具，用户
//     Provider 在 Build 内调用，返回 *http.Server 供停机 Shutdown）。
//
// 生命周期约定（用户 Provider 遵守）：阶段 B（BeforeStart，契约装载校验后）
// Build——必须晚于配置装载（值来自契约），早于 servers 监听（BeforeStart
// 语义保证），使 Recorder / tracer 中间件在首个请求前接入全局 Provider；
// cleanup 交引擎挂停机 Effect（canonical 序：metrics-server、tracer-shutdown
// 的回放位次与旧 12 段流一致——所有组件 cleanup 完成、Logger 恢复后，再停
// 暴露端、flush 尾批指标与 span）。两段均不支持热更新。
//
// 段缺省语义（契约级，框架保守缺省）：
//   - tracer 段缺省 → no-op trace（otel 全局默认，零配置可运行）；
//   - metrics 段缺省 → 不装配（无暴露端点）。
//   - 段存在而无 Provider → L3 覆盖检查启动期报错（原「Registry 未接线」
//     fail-fast 的等价物，见 segment_coverage.go）。
package appkit

import (
	"context"
	"net/http"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/log"
)

// TracerProvider 按契约 Tracer 段装配全局 trace 后端。
// 返回 shutdown（flush 缓冲 span，进程退出前调用），由引擎挂停机 Effect
// 逆序回放（最后执行，收集全部尾批 span）。未开启远端的实现返回 no-op
// shutdown。
type TracerProvider func(ctx context.Context, cfg *bootstrapv1.Tracer) (func(context.Context) error, error)

// MetricsProvider 按契约 Metrics 段装配指标后端。
// 返回 (handler, shutdown)：handler 是 Prometheus 抓取端（经
// StartMetricsServer 挂独立端口）；shutdown flush OTLP 直推通道并释放
// MeterProvider（进程退出前调用）。
type MetricsProvider func(ctx context.Context, cfg *bootstrapv1.Metrics) (http.Handler, func(context.Context) error, error)

// 指标暴露端缺省值（根治：与 gRPC :9090 错峰）。
const (
	defaultMetricsAddr = ":9091"
	defaultMetricsPath = "/metrics"
)

// StartMetricsServer 在独立端口起指标暴露端（Prometheus 抓取），返回
// *http.Server 供停机 Shutdown（与业务 server 生命周期解耦的独立监听面）。
// addr / path 空串走缺省（:9091 / /metrics）。监听失败异步记日志不 panic
// （旁路语义，与审计/指标一致——指标面挂了不该拖垮业务）。
func StartMetricsServer(addr, path string, h http.Handler) *http.Server {
	if addr == "" {
		addr = defaultMetricsAddr
	}
	if path == "" {
		path = defaultMetricsPath
	}
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(context.Background(), "metrics server stopped", "addr", addr, "error", err.Error())
		}
	}()
	return srv
}
