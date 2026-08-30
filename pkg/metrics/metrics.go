// Package metrics 定义 bald 的可观测性指标抽象（第三支柱，与 trace/log 并列）。
//
// 设计原则（与 log/audit 一致）：
//   - 零后端耦合：默认使用 otel 全局 MeterProvider（未配置时为 no-op，零配置可运行），
//     不 import 任何具体 exporter（prometheus/otlp 等外置为桥接子模块）。
//   - 与 M7 审计同源：指标维度直接复用 AuditEvent 的 (object, action, result)，
//     由拦截器在记录审计事件时同步 emit，避免重复解析。
//   - 指标语义：请求计数（按 transport/object/action/result/code）+ 请求延迟直方图
//     （按 transport/object/action），覆盖生产可观测性的「量 / 延迟 / 错误率」三要素。
package metrics

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Transport 标识请求来源（grpc / http），作为指标维度。
type Transport string

const (
	// TransportGRPC gRPC  unary/stream 请求。
	TransportGRPC Transport = "grpc"
	// TransportHTTP REST（含 gateway 转码）请求。
	TransportHTTP Transport = "http"
)

// Recorder 是 bald 指标记录器接口。具体 instrument 由核心用 otel 构建，默认 noop。
//
// 实现契约：Record 必须非阻塞或快速失败，绝不向上游抛错（与 audit.Auditor 对称——旁路）。
type Recorder interface {
	// Record 记录一次请求的指标（计数 + 延迟）。
	// ev 是 M7 的审计事件（提供 object/action/result/error）；transport 标识来源；
	// durationSeconds 为本次请求耗时（秒）。
	Record(ctx context.Context, ev Event, transport Transport, durationSeconds float64)
}

// Event 是喂给指标的精简事件视图（避免 metrics 包反向依赖 audit 包）。
// 字段与 audit.AuditEvent 对齐，由拦截器从审计事件拷贝而来。
type Event struct {
	Object string
	Action string
	Result string // allow / deny / error
	Error  string
}

// nopRecorder 静默默认实现（未配置具体 MeterProvider 时不产生副作用）。
type nopRecorder struct{}

func (nopRecorder) Record(context.Context, Event, Transport, float64) {}

// NopRecorder 返回静默默认 Recorder。
func NopRecorder() Recorder { return nopRecorder{} }

// otelRecorder 是基于 otel/metric 的真实 Recorder。
type otelRecorder struct {
	requests metric.Int64Counter
	latency  metric.Float64Histogram
}

// New 用全局 MeterProvider 构建真实 Recorder。meterName 通常为 "bald/<transport>"。
// 若 MeterProvider 为 no-op，instrument 退化为 no-op，Record 不产生副作用。
func New(meterName string) Recorder {
	m := otel.Meter(meterName)
	requests, _ := m.Int64Counter(
		"bald_requests_total",
		metric.WithDescription("Total bald handled requests, labeled by transport/object/action/result"),
	)
	latency, _ := m.Float64Histogram(
		"bald_request_duration_seconds",
		metric.WithDescription("bald request latency in seconds, labeled by transport/object/action"),
		metric.WithUnit("s"),
	)
	return &otelRecorder{requests: requests, latency: latency}
}

// Record 实现 Recorder：emit 计数与延迟（带上 transport/object/action/result 维度）。
func (r *otelRecorder) Record(ctx context.Context, ev Event, transport Transport, dur float64) {
	attrs := metric.WithAttributes(
		attribute.String("transport", string(transport)),
		attribute.String("object", ev.Object),
		attribute.String("action", ev.Action),
		attribute.String("result", ev.Result),
	)
	r.requests.Add(ctx, 1, attrs)
	r.latency.Record(ctx, dur, metric.WithAttributes(
		attribute.String("transport", string(transport)),
		attribute.String("object", ev.Object),
		attribute.String("action", ev.Action),
	))
}
