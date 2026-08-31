// Package trace 是 bald 的 OpenTelemetry trace 远端直推桥接，
// 自 go-bald-admin 范例 internal/observability/trace 晋升（P11，见 docs/devel/zh-CN/架构优化路线.md）。
//
// 核心 grpc/gin 中间件（bald/pkg/middleware/{grpc,gin}.Observability）已起 span 并注入
// trace_id/span_id，但仅当全局 TracerProvider 被设置时才会真正采样导出；未设置则 otel.Tracer
// 返回 no-op（零配置可运行）。本包负责把全局 TracerProvider 接到远端 APM（与指标桥接对称）：
// 通过 WithOTLPAddr 提供地址则挂 otlptracehttp exporter 直推，否则不设置（沿用核心 no-op）。
// 核心埋点零改动，仅装配全局 Provider——印证 bald「核心零后端耦合、可观测性由调用方接线」原则。
// 环境变量读取由调用方负责（范例读 BALD_ADMIN_OTLP_ADDR），本包只吃显式参数。
package trace

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

type options struct {
	otlpAddr    string
	serviceName string
}

// Option 配置 Setup。
type Option func(*options)

// WithOTLPAddr 挂 OTLP trace exporter 直推远端（空串=不挂，沿用核心 no-op tracer）。
// 地址解析与指标桥接一致：http(s):// 用完整 EndpointURL，裸 host:port 走 insecure。
func WithOTLPAddr(addr string) Option { return func(o *options) { o.otlpAddr = addr } }

// WithServiceName 设置 resource 的 service.name（默认 "bald-app"）。
func WithServiceName(name string) Option { return func(o *options) { o.serviceName = name } }

// Setup 初始化全局 TracerProvider 并设为 otel 全局（若开启 OTLP）。
// 应在请求处理前（main 装配期）调用一次。返回 shutdown 函数（进程退出时调用以 flush 缓冲，
// 建议经 appkit.Effect 或 BeforeStop 挂接）。未开 OTLP 时返回 no-op shutdown。
func Setup(opts ...Option) (func(context.Context) error, error) {
	cfg := options{serviceName: "bald-app"}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.otlpAddr == "" {
		// 未开启：返回 no-op shutdown，沿用核心默认 no-op tracer。
		return func(context.Context) error { return nil }, nil
	}

	var tOpts []otlptracehttp.Option
	if len(cfg.otlpAddr) > 7 && (cfg.otlpAddr[:7] == "http://" || (len(cfg.otlpAddr) > 8 && cfg.otlpAddr[:8] == "https://")) {
		tOpts = append(tOpts, otlptracehttp.WithEndpointURL(cfg.otlpAddr))
	} else {
		tOpts = append(tOpts, otlptracehttp.WithEndpoint(cfg.otlpAddr), otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(context.Background(), tOpts...)
	if err != nil {
		return nil, err
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.serviceName),
	))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}
