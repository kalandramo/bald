// Package trace 是 go-bald-admin 的可观测性 trace 桥接（OpenTelemetry 远端直推）。
//
// 核心 grpc/gin 中间件（bald/pkg/middleware/{grpc,gin}.Observability）已起 span 并注入
// trace_id/span_id，但仅当全局 TracerProvider 被设置时才会真正采样导出；未设置则 otel.Tracer
// 返回 no-op（零配置可运行）。本包负责把全局 TracerProvider 接到远端 APM（与 M9 指标对称）：
// 设 BALD_ADMIN_OTLP_ADDR 则挂 otlptracehttp exporter 直推，否则不设置（沿用核心 no-op）。
// 核心埋点零改动，仅范例装配全局 Provider——印证 bald「核心零后端耦合、可观测性由调用方接线」原则。
package trace

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Setup 初始化全局 TracerProvider 并设为 otel 全局（若开启 OTLP）。
// 应在请求处理前（main 装配期）调用一次。返回 shutdown 函数（进程退出时调用以 flush 缓冲）。
//
// 开关：BALD_ADMIN_OTLP_ADDR 非空才挂远端 exporter；否则不设全局 Provider，核心 span 走 no-op。
// 地址解析与 M9 指标一致：http(s):// 用 WithEndpointURL，裸 host:port 用 WithEndpoint+WithInsecure。
func Setup() (func(context.Context) error, error) {
	addr := os.Getenv("BALD_ADMIN_OTLP_ADDR")
	if addr == "" {
		// 未开启：返回 no-op shutdown，沿用核心默认 no-op tracer。
		return func(context.Context) error { return nil }, nil
	}

	var opts []otlptracehttp.Option
	if len(addr) > 7 && (addr[:7] == "http://" || (len(addr) > 8 && addr[:8] == "https://")) {
		opts = append(opts, otlptracehttp.WithEndpointURL(addr))
	} else {
		opts = append(opts, otlptracehttp.WithEndpoint(addr), otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(context.Background(), opts...)
	if err != nil {
		return nil, err
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName("go-bald-admin"),
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
