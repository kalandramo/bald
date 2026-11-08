// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// observability.go 是统一形态的可观测性中间件（方案 S 批次 2 第十件，自两链
// gin/grpc observability 合并核心语义）。semconv 收拢（observability-r09）后
// 解锁：span 起测、trace_id 落 ctx、头注入、跳过判定一份实现服务双协议；协议
// 丰富的外围（HTTP body 捕获、gRPC metadata 注入、日志载荷形状）按 Transporter.Kind
// 分派——这部分天生协议特有，合并的是「写两遍的核心」而非抹平协议差异。
//
// 两链 gin.Observability / grpc.{Unary,Stream}Observability 经三座桥委托本件，
// 公共 Option 面与钉住行为（SpanKindServer、no-op 兜底随机 trace_id、contextx 与
// 日志属性同值）保持不变。

package middleware

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/semconv"
)

// ObservabilityOption 配置统一可观测性中间件。
type ObservabilityOption func(*observabilityOptions)

type observabilityOptions struct {
	tracer            trace.Tracer
	traceMode         TraceInjectionMode
	customTraceHeader string
	logger            log.Logger
	disableBodyLog    bool
	stream            bool                  // gRPC 流式：不记 body、完成日志措辞不同
	skip              func(Transporter) bool // 跳过判定由协议侧注入（gin 路径+动词 / gRPC 方法）
}

// ObservabilityWithTracer 注入 tracer（缺省 otel.Tracer("bald/middleware")）。
// 两链委托时传入各自包级 tracer，保住「未装配全局 TracerProvider 时 no-op、
// 零配置可运行」语义与同包测试替换包级 tracer 的能力。
func ObservabilityWithTracer(t trace.Tracer) ObservabilityOption {
	return func(o *observabilityOptions) {
		if t != nil {
			o.tracer = t
		}
	}
}

// ObservabilityWithTraceInjection 设置 trace 头注入模式与自定义 trace ID 头名。
func ObservabilityWithTraceInjection(mode TraceInjectionMode, customHeader string) ObservabilityOption {
	return func(o *observabilityOptions) {
		o.traceMode = mode
		o.customTraceHeader = customHeader
	}
}

// ObservabilityWithLogger 显式注入 log.Logger（优先于全局惰性解析——注入后
// 运行期 SetLogger 重建不影响本中间件）。
func ObservabilityWithLogger(l log.Logger) ObservabilityOption {
	return func(o *observabilityOptions) {
		if l != nil {
			o.logger = l
		}
	}
}

// ObservabilityWithDisableBodyLog 强制不记请求/响应 body（gRPC 流式恒禁用）。
func ObservabilityWithDisableBodyLog(disable bool) ObservabilityOption {
	return func(o *observabilityOptions) { o.disableBodyLog = disable }
}

// ObservabilityWithStream 标记 gRPC 流式面：完成日志用 "gRPC stream completed"
// 且不记 body（流式 req 是 srv 而非消息，无 body 可记）。
func ObservabilityWithStream() ObservabilityOption {
	return func(o *observabilityOptions) {
		o.stream = true
		o.disableBodyLog = true
	}
}

// ObservabilityWithSkip 注入跳过判定（命中则不起 span、不记日志，直接透传）。
// gin 侧闭包按路径+动词匹配，gRPC 侧按完整方法名匹配—— matcher 语义留在协议侧。
func ObservabilityWithSkip(fn func(Transporter) bool) ObservabilityOption {
	return func(o *observabilityOptions) { o.skip = fn }
}

// resolveLogger 返回生效 Logger：显式注入优先，否则每次请求惰性取全局
// （构造期快照会让运行期 SetLogger 重建对请求日志永久失效）。
func (o *observabilityOptions) resolveLogger() log.Logger {
	if o.logger != nil {
		return o.logger
	}
	return log.GetLogger()
}

// Observability 是统一可观测性中间件：起 SpanKindServer span（上游有 span 则
// 作 child）、把 trace_id/span_id 同源写入 contextx 与 ctx 属性流、按模式注入
// trace 头、记一条结构化完成日志。span 名取 Transporter.Operation()（HTTP 是
// "METHOD /路由模板"、gRPC 是完整方法名——有界模板，raw path 只进 span 属性）。
//
// 须经三座桥装配（桥注入 Transporter）；无 Transporter 时退化为透传 next。
func Observability(opts ...ObservabilityOption) Middleware {
	cfg := &observabilityOptions{
		tracer:    otel.Tracer("bald/middleware"),
		traceMode: InjectTraceIDOnly,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return func(next Handler) Handler {
		return func(ctx context.Context, req any) (any, error) {
			tr, ok := FromServerContext(ctx)
			if !ok {
				return next(ctx, req)
			}
			if cfg.skip != nil && cfg.skip(tr) {
				return next(ctx, req)
			}

			start := time.Now()

			// SpanKindServer：inbound 的 OTel 语义（spanmetrics 服务端聚合与
			// 服务拓扑图依赖该值，缺省 internal 会被漏计）。
			ctx, span := cfg.tracer.Start(ctx, tr.Operation(), trace.WithSpanKind(trace.SpanKindServer))
			defer span.End()

			// trace_id/span_id 同源进 contextx（audit/authn/crudbridge 的
			// TraceIDFromContext 消费）与 ctx 属性流（日志自动携带）。no-op
			// tracer 下 LogTraceIDs 兜底随机 ID，零配置也有可关联的日志链路。
			logTraceID, logSpanID := LogTraceIDs(ctx)
			ctx = contextx.WithTraceID(ctx, logTraceID)
			ctx = log.ContextWithAttrs(ctx,
				slog.String("trace_id", logTraceID),
				slog.String("span_id", logSpanID),
			)
			spanCtx := trace.SpanContextFromContext(ctx)

			logger := cfg.resolveLogger()
			st := obsState{
				tr:            tr,
				span:          span,
				spanCtx:       spanCtx,
				cfg:           cfg,
				logger:        logger,
				isDebugLevel:  logger.Enabled(log.LevelDebug),
				logTraceID:    logTraceID,
				start:         start,
			}
			st.shouldLogBody = st.isDebugLevel && !cfg.disableBodyLog

			if tr.Kind() == KindHTTP {
				return st.observeHTTP(ctx, next, req)
			}
			return st.observeGRPC(ctx, next, req)
		}
	}
}

// obsState 承载一次请求的观测上下文（核心已算好的 span/trace_id/级别判定），
// 供 HTTP/gRPC 两面外围共用，避免长参数列表。
type obsState struct {
	tr            Transporter
	span          trace.Span
	spanCtx       trace.SpanContext
	cfg           *observabilityOptions
	logger        log.Logger
	isDebugLevel  bool
	shouldLogBody bool
	logTraceID    string
	start         time.Time
}

// observeHTTP 是 HTTP 面外围：trace 头进响应头、debug 级捕获请求/响应 body、
// semconv HTTP span 属性（状态取桥记录的终值，err 恒 nil——与旧 gin 一致）、
// "HTTP request completed" 日志。req 经 GinHandler 传入即 *gin.Context。
func (st obsState) observeHTTP(ctx context.Context, next Handler, req any) (any, error) {
	tr, cfg, span := st.tr, st.cfg, st.span
	InjectTrace(cfg.traceMode, cfg.customTraceHeader, st.spanCtx, tr.ReplyHeader().Set)

	var requestBody string
	var responseBuffer bytes.Buffer
	if c, ok := req.(*gin.Context); ok {
		if st.shouldLogBody && c.Request.Body != nil {
			bodyBytes, _ := io.ReadAll(c.Request.Body)
			requestBody = string(bodyBytes)
			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}
		if st.shouldLogBody {
			c.Writer = &bodyCaptureWriter{ResponseWriter: c.Writer, body: &responseBuffer}
		}
	}

	resp, err := next(ctx, req)

	duration := time.Since(st.start).Seconds()
	statusCode := tr.StatusCode()
	span.SetAttributes(semconv.HTTPSpanAttrs(
		tr.Method(), tr.Route(), tr.Path(), tr.Protocol(), statusCode, nil,
	)...)

	event := map[string]any{"duration": duration}
	httpData := map[string]any{
		"request": map[string]any{
			"method": tr.Method(),
			"path":   tr.Path(),
			"id":     st.logTraceID,
		},
		"response": map[string]any{
			"status_code": statusCode,
		},
	}
	var source map[string]any
	var userAgent map[string]any
	if c, ok := req.(*gin.Context); ok {
		source = map[string]any{"ip": c.ClientIP()}
		userAgent = map[string]any{"original": c.Request.UserAgent()}
	}
	if st.shouldLogBody {
		httpData["request"].(map[string]any)["body"] = map[string]any{
			"content": requestBody,
			"bytes":   len(requestBody),
		}
		httpData["response"].(map[string]any)["body"] = map[string]any{
			"content": responseBuffer.String(),
			"bytes":   responseBuffer.Len(),
		}
	}

	msg := "HTTP request completed"
	if st.isDebugLevel {
		st.logger.Debug(ctx, msg, "event", event, "source", source, "http", httpData, "user_agent", userAgent)
	} else {
		st.logger.Info(ctx, msg, "event", event, "source", source, "http", httpData, "user_agent", userAgent)
	}
	return resp, err
}

// observeGRPC 是 gRPC 面外围：trace 头进 incoming metadata、semconv RPC span
// 属性（wire 拼写状态 + error.type + reason）、"gRPC request/stream completed"
// 日志。unary 记 req/resp 对象作 body；stream（cfg.stream）不记 body。
func (st obsState) observeGRPC(ctx context.Context, next Handler, req any) (any, error) {
	tr, cfg, span := st.tr, st.cfg, st.span
	// 与旧 grpc 一致：仅在有 incoming metadata 时注入（md.Set 原地改同一张 map，
	// 再挂回 ctx）；无 metadata 不注入也不 panic。
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		InjectTrace(cfg.traceMode, cfg.customTraceHeader, st.spanCtx, func(key, value string) { md.Set(key, value) })
		ctx = metadata.NewIncomingContext(ctx, md)
	}

	var requestInfo any
	if st.shouldLogBody {
		requestInfo = req
	}

	resp, err := next(ctx, req)

	var responseInfo any
	if st.shouldLogBody {
		responseInfo = resp
	}

	duration := time.Since(st.start).Seconds()
	code := status.Code(err)
	span.SetAttributes(semconv.RPCAttrs(tr.Operation(), code, err)...)

	event := map[string]any{"duration": duration}
	source := map[string]any{"id": st.logTraceID}
	grpcData := map[string]any{
		"service": tr.Operation(),
		"code":    code.String(),
	}
	if st.shouldLogBody {
		grpcData["request"] = requestInfo
		grpcData["response"] = responseInfo
	}

	msg := "gRPC request completed"
	if cfg.stream {
		msg = "gRPC stream completed"
	}
	if st.isDebugLevel {
		st.logger.Debug(ctx, msg, "event", event, "source", source, "grpc", grpcData)
	} else {
		st.logger.Info(ctx, msg, "event", event, "source", source, "grpc", grpcData)
	}
	return resp, err
}

// bodyCaptureWriter 捕获并复制写响应 body（自旧 gin observability 并入）。
type bodyCaptureWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *bodyCaptureWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}