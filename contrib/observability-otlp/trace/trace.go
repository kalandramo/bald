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
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

type options struct {
	otlpAddr    string
	serviceName string
	// insecure 为三态：nil=按地址前缀推断（裸 host:port 走 insecure，http(s):// 按前缀），
	// true=强制 insecure，false=强制 TLS。对应 bconf 契约 Tracer.Otlp.insecure 字段。
	insecure    *bool
	headers     map[string]string
	sampler     string
	sampleRatio float64
}

// Option 配置 Setup。
type Option func(*options)

// WithOTLPAddr 挂 OTLP trace exporter 直推远端（空串=不挂，沿用核心 no-op tracer）。
// 地址解析与指标桥接一致：http(s):// 用完整 EndpointURL，裸 host:port 走 insecure。
func WithOTLPAddr(addr string) Option { return func(o *options) { o.otlpAddr = addr } }

// WithServiceName 设置 resource 的 service.name（默认 "bald-app"）。
func WithServiceName(name string) Option { return func(o *options) { o.serviceName = name } }

// WithInsecure 显式指定是否 insecure 连接，覆盖按地址前缀的推断。
func WithInsecure(insecure bool) Option {
	return func(o *options) { o.insecure = &insecure }
}

// WithHeaders 设置 OTLP exporter 请求头（远端 APM 鉴权场景，如 Grafana Cloud
// 的 Authorization: Bearer）。对应 bconf 契约 Tracer.Otlp.headers 字段。
func WithHeaders(headers map[string]string) Option {
	return func(o *options) { o.headers = headers }
}

// WithSampler 设置采样策略：always_on / always_off / trace_id_ratio / parent_based
// （对应 bconf 契约 Tracer.Otlp.sampler 字段）。空串与未知值退化为
// ParentBased(AlwaysSample)（OTel SDK 缺省），未知值额外 WARN。
func WithSampler(sampler string) Option { return func(o *options) { o.sampler = sampler } }

// WithSampleRatio 设置 trace_id_ratio 采样比率（0.0 ~ 1.0；非正值按 1.0 处理）。
func WithSampleRatio(ratio float64) Option { return func(o *options) { o.sampleRatio = ratio } }

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

	// 地址形式：http(s):// 前缀按完整 EndpointURL 解析，裸 host:port 走 WithEndpoint。
	isURL := len(cfg.otlpAddr) > 7 && (cfg.otlpAddr[:7] == "http://" || (len(cfg.otlpAddr) > 8 && cfg.otlpAddr[:8] == "https://"))
	var tOpts []otlptracehttp.Option
	if isURL {
		tOpts = append(tOpts, otlptracehttp.WithEndpointURL(cfg.otlpAddr))
	} else {
		tOpts = append(tOpts, otlptracehttp.WithEndpoint(cfg.otlpAddr))
	}
	// insecure 三态：显式设置优先（true=强制 insecure，false=强制 TLS），
	// 未显式时按地址推断（裸 host:port 默认 insecure，URL 按前缀）。
	insecure := cfg.insecure != nil && *cfg.insecure
	if cfg.insecure == nil && !isURL {
		insecure = true
	}
	if insecure {
		tOpts = append(tOpts, otlptracehttp.WithInsecure())
	}
	if len(cfg.headers) > 0 {
		tOpts = append(tOpts, otlptracehttp.WithHeaders(cfg.headers))
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
		sdktrace.WithSampler(buildSampler(cfg.sampler, cfg.sampleRatio)),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// buildSampler 按名称构造 SDK 采样器；空串/未知值退化为 OTel 缺省
// ParentBased(AlwaysSample)，未知值额外 WARN（宽容启动，不 fail）。
func buildSampler(name string, ratio float64) sdktrace.Sampler {
	switch name {
	case "", "parent_based":
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case "always_on":
		return sdktrace.AlwaysSample()
	case "always_off":
		return sdktrace.NeverSample()
	case "trace_id_ratio":
		if ratio <= 0 {
			ratio = 1.0
		}
		return sdktrace.TraceIDRatioBased(ratio)
	default:
		slog.Warn("unknown trace sampler, falling back to parent_based(always_on)", "sampler", name)
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	}
}
