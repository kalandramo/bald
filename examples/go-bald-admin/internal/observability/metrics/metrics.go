// Package metrics 是 go-bald-admin 的可观测性指标桥接（bald metrics.Recorder 的真实后端）。
//
// 采用 OpenTelemetry 多 Reader 模式：默认挂 Prometheus exporter（供 /metrics 被抓取），
// 若设置 BALD_ADMIN_OTLP_ADDR 则额外挂 OTLP metric exporter 直推远端 APM（如 VictoriaMetrics /
// Grafana Cloud / OTel Collector）。两种后端共用同一全局 MeterProvider，拦截器通过
// metrics.New("bald/example") 创建的 Recorder 写入后由各自 Reader 分发。这是真实副作用
// （非 fake/stub），符合 §0。
package metrics

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/kalandramo/bald/pkg/metrics"
)

// Setup 初始化 MeterProvider 并设为全局，返回 /metrics HTTP handler（Prometheus 抓取端点）。
// 应在 AppKit 装配早期调用一次；返回的 handler 挂到独立端口或 gin 路由。
//
// 后端策略（M9 OTLP 直推，核心 Recorder 不变）：
//   - 始终挂 Prometheus exporter（/metrics 端点，本地可观测）。
//   - 若环境变量 BALD_ADMIN_OTLP_ADDR 非空，额外挂 OTLP metric exporter（periodic reader，
//     默认 15s 上报）直推远端 APM；地址可为 collector 裸地址（如 http://otel-collector:4318）
//     或带协议前缀，otel 客户端自动补 /v1/metrics 路径。
func Setup() (http.Handler, error) {
	readers := []metric.Reader{}

	// 1) Prometheus：本地抓取端点（必需）。
	promExporter, err := prometheus.New()
	if err != nil {
		return nil, err
	}
	readers = append(readers, promExporter)

	// 2) OTLP：远端 APM（可选，按环境变量开启）。
	if addr := os.Getenv("BALD_ADMIN_OTLP_ADDR"); addr != "" {
		otlpExporter, err := newOTLPExporter(addr)
		if err != nil {
			return nil, err
		}
		readers = append(readers, metric.NewPeriodicReader(otlpExporter,
			metric.WithInterval(15*time.Second)))
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName("go-bald-admin"),
	))
	if err != nil {
		return nil, err
	}
	opts := []metric.Option{
		metric.WithResource(res),
	}
	for _, r := range readers {
		opts = append(opts, metric.WithReader(r))
	}
	provider := metric.NewMeterProvider(opts...)
	otel.SetMeterProvider(provider)
	// 返回 Prometheus 标准暴露端点（/metrics）。
	return promhttp.Handler(), nil
}

// newOTLPExporter 按地址构造 OTLP metric HTTP exporter（M9）。
// 支持裸地址（http://host:port）与带路径地址；otel 客户端自动定位 /v1/metrics。
func newOTLPExporter(addr string) (*otlpmetrichttp.Exporter, error) {
	opts := []otlpmetrichttp.Option{}
	// 仅当显式含 http(s):// 时按完整 Endpoint 解析，否则当作 host:port 透传。
	if len(addr) > 7 && (addr[:7] == "http://" || (len(addr) > 8 && addr[:8] == "https://")) {
		opts = append(opts, otlpmetrichttp.WithEndpointURL(addr))
	} else {
		opts = append(opts, otlpmetrichttp.WithEndpoint(addr), otlpmetrichttp.WithInsecure())
	}
	return otlpmetrichttp.New(context.Background(), opts...)
}

// Recorder 返回基于全局 MeterProvider 的真实指标记录器（监听 bald_requests_total /
// bald_request_duration_seconds）。须在 Setup 之后构建以接入 exporter。
func Recorder() metrics.Recorder {
	return metrics.New("bald/example")
}
