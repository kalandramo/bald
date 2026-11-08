// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// audit.go 是统一形态的审计中间件（自两链合并核心语义；方案 S 批次 2）。

package middleware

import (
	"context"
	"time"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/metrics"
)

// AuditOption 配置统一审计中间件的请求→审计三元组提取方式（与 Authz 对称）。
// 复用归一化原语，使审计与授权在同一份语义（object/action）下记录。
type AuditOption func(*auditOptions)

type auditOptions struct {
	objectResolver  authz.ObjectResolver
	actionResolver  authz.ActionResolver
	subjectResolver func(ctx context.Context, operation string) string
	auditor         audit.Auditor
	metricsRecorder metrics.Recorder
}

// AuditWithObjectResolver 自定义 object 推导（operation 为入参）。
func AuditWithObjectResolver(fn authz.ObjectResolver) AuditOption {
	return func(o *auditOptions) { o.objectResolver = fn }
}

// AuditWithActionResolver 自定义 action 推导。
func AuditWithActionResolver(fn authz.ActionResolver) AuditOption {
	return func(o *auditOptions) { o.actionResolver = fn }
}

// AuditWithSubjectResolver 自定义 subject 提取。
func AuditWithSubjectResolver(fn func(ctx context.Context, operation string) string) AuditOption {
	return func(o *auditOptions) { o.subjectResolver = fn }
}

// AuditWithAuditor 指定审计后端；缺省 NopAuditor（显式注入是唯一通道）。
func AuditWithAuditor(a audit.Auditor) AuditOption {
	return func(o *auditOptions) { o.auditor = a }
}

// AuditWithMetrics 同步 emit 指标（与审计同源，复用 object/action/result 维度）；
// nil 时使用 metrics.NopRecorder()。旁路副作用，失败不影响业务。
func AuditWithMetrics(rec metrics.Recorder) AuditOption {
	return func(o *auditOptions) { o.metricsRecorder = rec }
}

// Audit 是统一审计中间件（旁路，不阻断业务）：在 next 返回后记录一条
// AuditEvent——subject/tenant 来自 AuthClaims，object/action 默认取自
// Transporter.Operation()（可用 Resolver 接入归一化），Result 由统一链返回的
// error 推导（PermissionDenied→deny、其它 error→error、nil→allow）——统一链
// 直接持有错误本身，不再需要 gin 的状态码推导或 grpc 的 status 二次反解。
//
// 协议丰富的外围（active_requests、client_ip/user_agent、semconv requestInfo）
// 仍留在两链旧实现，待 semconv 收拢统一后并入；本件承载的是审计的语义核心。
//
// 审计写入失败仅降级记日志，不影响响应。
func Audit(opts ...AuditOption) Middleware {
	cfg := &auditOptions{}
	for _, opt := range opts {
		opt(cfg)
	}
	auditor := cfg.auditor
	if auditor == nil {
		auditor = audit.NopAuditor()
	}
	rec := cfg.metricsRecorder
	if rec == nil {
		rec = metrics.NopRecorder()
	}
	return func(next Handler) Handler {
		return func(ctx context.Context, _ interface{}) (interface{}, error) {
			start := time.Now()

			operation := "unknown"
			transport := metrics.TransportHTTP
			if tr, ok := FromServerContext(ctx); ok {
				operation = tr.Operation()
				if tr.Kind() == KindGRPC {
					transport = metrics.TransportGRPC
				}
			}

			subject, tenant := auditSubjectTenant(ctx, cfg, operation)
			object, action := auditObjectAction(cfg, operation, transport)

			resp, err := next(ctx, nil)

			ev := audit.AuditEvent{
				Time:     start,
				Subject:  subject,
				TenantID: tenant,
				Object:   object,
				Action:   action,
				Result:   audit.ResultAllow,
				Meta: map[string]any{
					"request_id": contextx.RequestIDFromContext(ctx),
					"trace_id":   contextx.TraceIDFromContext(ctx),
					"operation":  operation,
				},
			}
			if err != nil {
				ev.Result = audit.ResultError
				// 拒绝：berrors 错误直接识别（统一链持有错误本身，无需 status
				// 二次反解）。
				if be, ok := berrors.FromError(err); ok && be.Code == berrors.CodePermissionDenied {
					ev.Result = audit.ResultDeny
				}
				ev.Error = err.Error()
			}
			recordSafely(auditor, ctx, ev)

			// 指标埋点：审计三元组走 bald_audit_events_total（正交拆分，见
			// docs/devel/zh-CN/Bald 指标设计.md），旁路不阻断。transport 维度由
			// Transporter.Kind 派生。
			emitMetricsSafely(rec, ctx, metrics.Event{
				Object: object,
				Action: action,
				Result: string(ev.Result),
				Error:  ev.Error,
				Request: metrics.RequestInfo{
					StatusCode: statusHint(err),
				},
			}, transport, time.Since(start).Seconds())
			return resp, err
		}
	}
}

// auditObjectAction 三元组的 object/action 默认：object=operation（gRPC 完整
// 方法名或 "METHOD /路由模板"），action=HTTP 动词或 gRPC "CALL"。
func auditObjectAction(cfg *auditOptions, operation string, transport metrics.Transport) (string, string) {
	object, action := operation, "CALL"
	if transport == metrics.TransportHTTP {
		action = "GET"
		if i := lastSpace(operation); i >= 0 {
			object = operation[i+1:]
			action = operation[:i]
		}
	}
	if cfg.objectResolver != nil {
		object = cfg.objectResolver(operation)
	}
	if cfg.actionResolver != nil {
		action = cfg.actionResolver(operation)
	}
	return object, action
}

// lastSpace 返回 s 中最后一个空格的下标（"GET /v1/x" → 3）；无空格返回 -1。
func lastSpace(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ' ' {
			return i
		}
	}
	return -1
}

func auditSubjectTenant(ctx context.Context, cfg *auditOptions, operation string) (string, string) {
	if cfg.subjectResolver != nil {
		return cfg.subjectResolver(ctx, operation), ""
	}
	if cl := authn.AuthClaimsFromContext(ctx); cl != nil {
		return cl.Subject, cl.TenantID
	}
	return "", ""
}

// statusHint 从统一链错误推导协议无关的观测状态（PermissionDenied→403、
// ResourceExhausted→429、其余错误→500、成功→200），供指标 status 维度。
func statusHint(err error) int {
	if err == nil {
		return 200
	}
	if be, ok := berrors.FromError(err); ok {
		switch be.Code {
		case berrors.CodePermissionDenied:
			return 403
		case berrors.CodeResourceExhausted:
			return 429
		case berrors.CodeUnauthenticated:
			return 401
		case berrors.CodeInvalidArgument:
			return 400
		}
	}
	return 500
}

// emitMetricsSafely 指标旁路记录：Recorder panic 仅降级记日志，绝不影响响应。
func emitMetricsSafely(rec metrics.Recorder, ctx context.Context, ev metrics.Event, transport metrics.Transport, seconds float64) {
	if rec == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Error(ctx, "metrics recorder panicked", "panic", r)
		}
	}()
	rec.Record(ctx, ev, transport, seconds)
}

// recordSafely 旁路记录：Auditor 报错仅降级记日志，绝不向上游抛错。
func recordSafely(auditor audit.Auditor, ctx context.Context, ev audit.AuditEvent) {
	if auditor == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			log.Error(ctx, "auditor panicked", "panic", rec)
		}
	}()
	auditor.Record(ctx, ev)
}
