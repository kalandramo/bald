// Package metrics 是 bald metrics.Recorder 的 OpenTelemetry 真实后端桥接，
// 自 go-bald-admin 范例 internal/observability/metrics 晋升（P11，见 docs/devel/zh-CN/架构优化路线.md）。
//
// 采用 OTel 多 Reader 模式：默认挂 Prometheus exporter（供 /metrics 被抓取），
// 若通过 WithOTLPAddr 提供地址则额外挂 OTLP metric exporter 直推远端 APM（如
// VictoriaMetrics / Grafana Cloud / OTel Collector）。两种后端共用同一全局
// MeterProvider，bald 核心 metrics.New(scope) 创建的 Recorder 写入后由各自 Reader 分发。
// 这是真实副作用（非 fake/stub）。环境变量读取由调用方负责（范例读 BALD_ADMIN_OTLP_ADDR），
// 本包只吃显式参数——可观测性接线遵循 bald「核心零后端耦合、由调用方装配」原则。
package metrics

import (
	"context"
	"net/http"
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

// 默认值。
const (
	defaultServiceName = "bald-app"
	defaultInterval    = 15 * time.Second
)

type options struct {
	otlpAddr    string
	serviceName string
	interval    time.Duration
}

// Option 配置 Setup。
type Option func(*options)

// WithOTLPAddr 额外挂 OTLP metric exporter 直推远端（空串=不挂，仅 Prometheus）。
// 地址可为 collector 裸地址（host:port，走 insecure）或带 http(s):// 前缀的完整地址；
// otel 客户端自动补 /v1/metrics 路径。
func WithOTLPAddr(addr string) Option { return func(o *options) { o.otlpAddr = addr } }

// WithServiceName 设置 resource 的 service.name（默认 "bald-app"）。
func WithServiceName(name string) Option { return func(o *options) { o.serviceName = name } }

// WithInterval 设置 OTLP periodic reader 上报间隔（默认 15s）。
func WithInterval(d time.Duration) Option { return func(o *options) { o.interval = d } }

// Setup 初始化 MeterProvider 并设为全局，返回 /metrics HTTP handler（Prometheus 抓取端点）。
// 应在拦截器构建**之前**调用一次（使埋点接入 exporter）；返回的 handler 挂到独立端口或路由。
func Setup(opts ...Option) (http.Handler, error) {
	cfg := options{serviceName: defaultServiceName, interval: defaultInterval}
	for _, opt := range opts {
		opt(&cfg)
	}

	readers := []metric.Reader{}

	// 1) Prometheus：本地抓取端点（必需）。
	promExporter, err := prometheus.New()
	if err != nil {
		return nil, err
	}
	readers = append(readers, promExporter)

	// 2) OTLP：远端 APM（可选，按 WithOTLPAddr 开启）。
	if cfg.otlpAddr != "" {
		otlpExporter, err := newOTLPExporter(cfg.otlpAddr)
		if err != nil {
			return nil, err
		}
		readers = append(readers, metric.NewPeriodicReader(otlpExporter,
			metric.WithInterval(cfg.interval)))
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.serviceName),
	))
	if err != nil {
		return nil, err
	}
	mopts := []metric.Option{
		metric.WithResource(res),
	}
	// 注意：metric.WithReader 非可变参，多 Reader 须逐项 append（M9 踩坑记录）。
	for _, r := range readers {
		mopts = append(mopts, metric.WithReader(r))
	}
	provider := metric.NewMeterProvider(mopts...)
	otel.SetMeterProvider(provider)
	// 返回 Prometheus 标准暴露端点（/metrics）。
	return promhttp.Handler(), nil
}

// newOTLPExporter 按地址构造 OTLP metric HTTP exporter。
// http(s):// 前缀按完整 EndpointURL 解析；裸 host:port 走 WithEndpoint+WithInsecure。
func newOTLPExporter(addr string) (*otlpmetrichttp.Exporter, error) {
	opts := []otlpmetrichttp.Option{}
	if len(addr) > 7 && (addr[:7] == "http://" || (len(addr) > 8 && addr[:8] == "https://")) {
		opts = append(opts, otlpmetrichttp.WithEndpointURL(addr))
	} else {
		opts = append(opts, otlpmetrichttp.WithEndpoint(addr), otlpmetrichttp.WithInsecure())
	}
	return otlpmetrichttp.New(context.Background(), opts...)
}

// Recorder 返回基于全局 MeterProvider 的真实指标记录器（监听 bald_requests_total /
// bald_request_duration_seconds）。scope 为 meter 名（通常 "bald" 或 "<service>"）。
// 须在 Setup 之后调用以接入 exporter。
func Recorder(scope string) metrics.Recorder {
	return metrics.New(scope)
}
