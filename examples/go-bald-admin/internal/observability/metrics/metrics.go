// Package metrics 是 go-bald-admin 的可观测性指标桥接（bald metrics.Recorder 的真实后端）。
//
// 采用 OpenTelemetry Prometheus exporter：把全局 MeterProvider 设为 prometheus 后端，
// 拦截器通过 metrics.New("bald/example") 创建的 Recorder 直接把请求计数/延迟写入该 provider，
// 再由 /metrics 端点暴露给 Prometheus 抓取。这是真实副作用（非 fake/stub），符合 §0。
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"

	"github.com/kalandramo/bald/pkg/metrics"
)

// Setup 初始化 Prometheus MeterProvider 并设为全局，返回 /metrics HTTP handler。
// 应在 AppKit 装配早期调用一次；返回的 handler 挂到独立端口或 gin 路由。
func Setup() (http.Handler, error) {
	exporter, err := prometheus.New()
	if err != nil {
		return nil, err
	}
	provider := metric.NewMeterProvider(metric.WithReader(exporter))
	otel.SetMeterProvider(provider)
	// 返回 Prometheus 标准暴露端点（/metrics）。
	return promhttp.Handler(), nil
}

// Recorder 返回基于全局 MeterProvider 的真实指标记录器（监听 bald_requests_total /
// bald_request_duration_seconds）。须在 Setup 之后构建以接入 exporter。
func Recorder() metrics.Recorder {
	return metrics.New("bald/example")
}
