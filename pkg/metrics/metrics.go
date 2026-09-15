// Package metrics 定义 bald 的可观测性指标抽象（第三支柱，与 trace/log 并列）。
//
// 设计原则（与 log/audit 一致）：
//   - 零后端耦合：默认使用 otel 全局 MeterProvider（未配置时为 no-op，零配置可运行），
//     不 import 任何具体 exporter（prometheus/otlp 等外置为桥接子模块）。
//   - 协议指标对齐 OTel semconv v1.43.0：HTTP 侧 http.server.request.duration /
//     http.server.active_requests，gRPC 侧 rpc.server.call.duration——可套社区
//     dashboard、被 APM 自动识别（设计见 docs/devel/zh-CN/Bald 指标设计.md）。
//   - 审计三元组（object/action/result）与协议维度正交：业务视角走独立序列
//     bald_audit_events_total（M7 同源不丢），不与协议属性叠加（基数纪律）。
package metrics

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc/codes"
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
// 实现契约：Record / RecordActive 必须非阻塞或快速失败，绝不向上游抛错
// （与 audit.Auditor 对称——旁路）。
type Recorder interface {
	// Record 记录一次请求的指标（协议延迟 + 审计事件计数）。
	// ev 是 M7 的审计事件视图（object/action/result/error）+ 协议维度（Request）；
	// transport 标识来源；durationSeconds 为本次请求耗时（秒）。
	Record(ctx context.Context, ev Event, transport Transport, durationSeconds float64)

	// RecordActive 维护在途请求数（http.server.active_requests）。
	// ev 只需填 Request 部分（请求开始时 result/object 尚未产生）；
	// delta 请求开始 +1、结束 -1。semconv 仅定义 HTTP 侧，gRPC 侧调用为 no-op。
	RecordActive(ctx context.Context, ev Event, transport Transport, delta int64)
}

// Event 是喂给指标的精简事件视图（避免 metrics 包反向依赖 audit 包）。
// 审计三元组字段与 audit.AuditEvent 对齐，由拦截器从审计事件拷贝而来。
type Event struct {
	Object string
	Action string
	Result string // allow / deny / error
	Error  string

	// Request 是协议维度（semconv v1.43.0 对齐），由拦截器从请求上下文提取。
	// 零值兼容：不填则协议指标缺对应属性（不报错）。
	Request RequestInfo
}

// RequestInfo 是协议维度视图：HTTP 与 gRPC 共用一字段集，按 transport 取用。
type RequestInfo struct {
	// Method 是 HTTP 方法（"GET"…，http.request.method）或 gRPC FullMethod
	// （"/pkg.Service/Method"，rpc.method 收 fully-qualified 名）。
	Method string
	// Scheme 是 HTTP scheme（"http"/"https"，url.scheme）；gRPC 空。
	Scheme string
	// Template 是 HTTP 路由模板（"/v1/secret/:id"，url.template）；gRPC 空。
	Template string
	// StatusCode 是 HTTP 状态码（200/403…，http.response.status_code）或
	// gRPC codes.Code 数值（emit 时转 string，rpc.response.status_code）。
	StatusCode int
	// ServerAddr 是 HTTP Host 的 host 部分（server.address）；gRPC 空。
	ServerAddr string
	// ServerPort 是 HTTP Host 的 port 部分（server.port）；0 = 未知，省略属性。
	ServerPort int
}

// nopRecorder 静默默认实现（未配置具体 MeterProvider 时不产生副作用）。
type nopRecorder struct{}

func (nopRecorder) Record(context.Context, Event, Transport, float64)          {}
func (nopRecorder) RecordActive(context.Context, Event, Transport, int64)     {}

// NopRecorder 返回静默默认 Recorder。
func NopRecorder() Recorder { return nopRecorder{} }

// otelRecorder 是基于 otel/metric 的真实 Recorder。
//
// instruments 惰性创建（首次 Record 时）：otel 全局 MeterProvider 语义下，
// SetMeterProvider 之前创建的 instrument 永久 no-op（global 包的 delegating
// meter 只转发后续创建）。Recorder 常在 main 构造期创建（如 bundle.Metrics
// 接线），而 Provider 在 BeforeStart 装配——惰性一跳消除创建顺序耦合。
type otelRecorder struct {
	meterName string
	once      sync.Once

	httpDuration metric.Float64Histogram  // http.server.request.duration (s)
	rpcDuration  metric.Float64Histogram  // rpc.server.call.duration (s)
	activeReqs   metric.Int64UpDownCounter // http.server.active_requests ({request})
	auditEvents  metric.Int64Counter       // bald_audit_events_total
}

// New 用全局 MeterProvider 构建真实 Recorder。meterName 通常为 "bald/<transport>"。
// 若 MeterProvider 为 no-op，instrument 退化为 no-op，Record 不产生副作用。
func New(meterName string) Recorder {
	return &otelRecorder{meterName: meterName}
}

// instruments 首次使用时创建（见 otelRecorder 文档：创建顺序无关性）。
// 指标名与属性对齐 OTel semconv v1.43.0（httpconv/rpcconv 生成物）。
func (r *otelRecorder) init() {
	m := otel.Meter(r.meterName)
	r.httpDuration, _ = m.Float64Histogram("http.server.request.duration",
		metric.WithDescription("Duration of HTTP server requests."),
		metric.WithUnit("s"),
	)
	r.rpcDuration, _ = m.Float64Histogram("rpc.server.call.duration",
		metric.WithDescription("Measures the duration of an incoming Remote Procedure Call (RPC)."),
		metric.WithUnit("s"),
	)
	r.activeReqs, _ = m.Int64UpDownCounter("http.server.active_requests",
		metric.WithDescription("Number of active HTTP server requests."),
		metric.WithUnit("{request}"),
	)
	r.auditEvents, _ = m.Int64Counter("bald_audit_events_total",
		metric.WithDescription("Total bald audit events, labeled by transport/object/action/result."),
	)
}

// Record 实现 Recorder：按 transport 分流 emit 协议延迟（semconv 属性集），
// 再统一 emit 审计事件计数（正交的业务维度序列）。
func (r *otelRecorder) Record(ctx context.Context, ev Event, t Transport, dur float64) {
	r.once.Do(r.init)
	switch t {
	case TransportHTTP:
		attrs := []attribute.KeyValue{
			attribute.String("http.request.method", ev.Request.Method),
			attribute.String("url.scheme", ev.Request.Scheme),
		}
		if ev.Request.Template != "" {
			attrs = append(attrs, attribute.String("url.template", ev.Request.Template))
		}
		if ev.Request.StatusCode > 0 {
			attrs = append(attrs, attribute.Int("http.response.status_code", ev.Request.StatusCode))
		}
		r.httpDuration.Record(ctx, dur, metric.WithAttributes(attrs...))
	case TransportGRPC:
		attrs := []attribute.KeyValue{
			attribute.String("rpc.system.name", "grpc"),
		}
		if ev.Request.Method != "" {
			attrs = append(attrs, attribute.String("rpc.method", ev.Request.Method))
		}
		// semconv v1.43.0 的 rpc.response.status_code 是 string（"OK"/"PERMISSION_DENIED"…）。
		attrs = append(attrs, attribute.String("rpc.response.status_code", grpcStatusCode(ev.Request.StatusCode)))
		r.rpcDuration.Record(ctx, dur, metric.WithAttributes(attrs...))
	}
	r.auditEvents.Add(ctx, 1, metric.WithAttributes(
		attribute.String("transport", string(t)),
		attribute.String("object", ev.Object),
		attribute.String("action", ev.Action),
		attribute.String("result", ev.Result),
	))
}

// RecordActive 实现 Recorder：维护 http.server.active_requests（仅 HTTP）。
func (r *otelRecorder) RecordActive(ctx context.Context, ev Event, t Transport, delta int64) {
	if t != TransportHTTP {
		return
	}
	r.once.Do(r.init)
	attrs := []attribute.KeyValue{
		attribute.String("url.scheme", ev.Request.Scheme),
	}
	if ev.Request.ServerAddr != "" {
		attrs = append(attrs, attribute.String("server.address", ev.Request.ServerAddr))
	}
	if ev.Request.ServerPort > 0 {
		attrs = append(attrs, attribute.Int("server.port", ev.Request.ServerPort))
	}
	r.activeReqs.Add(ctx, delta, metric.WithAttributes(attrs...))
}

// grpcStatusCode 把 gRPC codes.Code 数值转为 semconv string 形态
// （"OK"/"PERMISSION_DENIED"…）。codes.Code(n).String() 对未知数值回退
// "Code(N)"（不 panic——旁路纪律）。
func grpcStatusCode(code int) string {
	return codes.Code(code).String()
}
