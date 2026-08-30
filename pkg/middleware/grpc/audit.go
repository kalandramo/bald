package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	berrors "github.com/kalandramo/bald/pkg/berrors"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/log"
	"github.com/kalandramo/bald/pkg/metrics"
)

// AuditOption 配置 AuditInterceptor 的请求→审计三元组提取方式（与 AuthzInterceptor 对称）。
// 复用 P9 的归一化原语，使审计与授权在同一份语义（object/action）下记录。
type AuditOption func(*auditOptions)

type auditOptions struct {
	objectResolver authz.ObjectResolver
	actionResolver authz.ActionResolver
	subjectResolver func(ctx context.Context, info *grpc.UnaryServerInfo) string
	// auditor 指定审计后端；nil 时使用 audit.GetAuditor() 全局实例。
	auditor audit.Auditor
	// metricsRecorder 同步 emit 指标（与审计同源）；nil 时 no-op。
	metricsRecorder metrics.Recorder
}

// AuditWithObjectResolver 自定义 object 推导；常用 authz.DefaultGRPCObject。
func AuditWithObjectResolver(fn authz.ObjectResolver) AuditOption {
	return func(o *auditOptions) { o.objectResolver = fn }
}

// AuditWithActionResolver 自定义 action 推导；常用 authz.DefaultGRPCAction。
func AuditWithActionResolver(fn authz.ActionResolver) AuditOption {
	return func(o *auditOptions) { o.actionResolver = fn }
}

// AuditWithSubjectResolver 自定义 subject 提取。
func AuditWithSubjectResolver(fn func(ctx context.Context, info *grpc.UnaryServerInfo) string) AuditOption {
	return func(o *auditOptions) { o.subjectResolver = fn }
}

// AuditWithAuditor 指定审计后端；缺省用全局 audit.GetAuditor()。
func AuditWithAuditor(a audit.Auditor) AuditOption {
	return func(o *auditOptions) { o.auditor = a }
}

// AuditWithMetrics 同步 emit 指标（与审计同源，复用 object/action/result 维度）；
// nil 时使用 metrics.NopRecorder()。旁路副作用，失败不影响业务。
func AuditWithMetrics(rec metrics.Recorder) AuditOption {
	return func(o *auditOptions) { o.metricsRecorder = rec }
}

// AuditInterceptor 是 gRPC 审计拦截器（旁路，不阻断业务）。
//
// 在 handler 返回后记录一条 AuditEvent：subject/tenant 来自 AuthClaims，object/action
// 默认取自 FullMethod/"CALL"（与 AuthzInterceptor 默认一致），可用 AuditWithObjectResolver/
// AuditWithActionResolver 接入 P9 归一化；Result 由返回 error 推导（PermissionDenied→deny、
// 其它 error→error、nil→allow）。审计写入失败仅记日志，不影响响应。
//
// 典型用法（与 AuthzInterceptor 归一化对称）：
//
//	grpcmw.AuditInterceptor(nil,
//	    grpcmw.AuditWithObjectResolver(authz.DefaultGRPCObject),
//	    grpcmw.AuditWithActionResolver(authz.DefaultGRPCAction),
//	)
func AuditInterceptor(_ audit.Auditor, opts ...AuditOption) grpc.UnaryServerInterceptor {
	cfg := &auditOptions{}
	for _, opt := range opts {
		opt(cfg)
	}
	auditor := cfg.auditor
	if auditor == nil {
		auditor = audit.GetAuditor()
	}
	resolver := newAuditResolver(cfg)
	rec := cfg.metricsRecorder
	if rec == nil {
		rec = metrics.NopRecorder()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		start := time.Now()
		subject, tenant := resolver.subjectTenant(ctx, info)
		object, action := resolver.objectAction(info)
		resp, err = handler(ctx, req)
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
			},
		}
		if err != nil {
			ev.Result = audit.ResultError
			// 拒绝：原生 berrors 或经 ErrorInterceptor 转码后的 status 均识别。
			if be, ok := berrors.FromError(err); ok && be.Code == berrors.CodePermissionDenied {
				ev.Result = audit.ResultDeny
			} else if status.Code(err) == codes.PermissionDenied {
				ev.Result = audit.ResultDeny
			}
			ev.Error = err.Error()
		}
		recordSafely(auditor, ctx, ev)
		// M8 指标埋点：与审计同源，复用 object/action/result 维度，旁路不阻断。
		rec.Record(ctx, metrics.Event{Object: object, Action: action, Result: string(ev.Result), Error: ev.Error}, metrics.TransportGRPC, time.Since(start).Seconds())
		return resp, err
	}
}

// auditResolver 缓存解析函数，避免每次请求重建闭包。
type auditResolver struct {
	cfg *auditOptions
}

func newAuditResolver(cfg *auditOptions) *auditResolver { return &auditResolver{cfg: cfg} }

func (r *auditResolver) subjectTenant(ctx context.Context, info *grpc.UnaryServerInfo) (string, string) {
	if r.cfg.subjectResolver != nil {
		return r.cfg.subjectResolver(ctx, info), ""
	}
	if c := authn.AuthClaimsFromContext(ctx); c != nil {
		return c.Subject, c.TenantID
	}
	return "", ""
}

func (r *auditResolver) objectAction(info *grpc.UnaryServerInfo) (string, string) {
	object, action := "", "CALL"
	if info != nil {
		object = info.FullMethod
		if r.cfg.objectResolver != nil {
			object = r.cfg.objectResolver(info.FullMethod)
		}
		if r.cfg.actionResolver != nil {
			action = r.cfg.actionResolver(info.FullMethod)
		}
	}
	return object, action
}

// recordSafely 旁路记录：Auditor 报错仅降级记日志，绝不向上游抛错。
func recordSafely(auditor audit.Auditor, ctx context.Context, ev audit.AuditEvent) {
	if auditor == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			log.GetLogger().Error(ctx, "auditor panicked", "panic", rec)
		}
	}()
	auditor.Record(ctx, ev)
}
