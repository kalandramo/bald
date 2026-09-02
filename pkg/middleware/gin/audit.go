package gin

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/log"
	"github.com/kalandramo/bald/pkg/metrics"
)

// AuditOption 配置 AuditMiddleware 的请求→审计三元组提取方式（与 AuthzMiddleware 对称）。
// 复用 P9 归一化原语，使审计与授权在同一份语义下记录。
type AuditOption func(*auditOptions)

type auditOptions struct {
	objectResolver  authz.ObjectResolver
	actionResolver  authz.ActionResolver
	subjectResolver func(c *gin.Context) string
	auditor         audit.Auditor
	metricsRecorder metrics.Recorder
}

// AuditWithObjectResolver 自定义 object 推导；常用 authz.DefaultHTTPObject。
func AuditWithObjectResolver(fn authz.ObjectResolver) AuditOption {
	return func(o *auditOptions) { o.objectResolver = fn }
}

// AuditWithActionResolver 自定义 action 推导；常用 authz.DefaultHTTPAction。
func AuditWithActionResolver(fn authz.ActionResolver) AuditOption {
	return func(o *auditOptions) { o.actionResolver = fn }
}

// AuditWithSubjectResolver 自定义 subject 提取。
func AuditWithSubjectResolver(fn func(c *gin.Context) string) AuditOption {
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

// AuditMiddleware 是 gin 审计中间件（旁路，不阻断业务）。
//
// 在 c.Next() 返回后记录一条 AuditEvent：subject/tenant 来自 AuthClaims，object/action
// 默认取自 path/HTTP 方法（与 AuthzMiddleware 默认一致），可用 AuditWithObjectResolver/
// AuditWithActionResolver 接入 P9 归一化；Result 由响应状态码推导（401/403→deny、
// >=500→error、其余→allow）。审计写入失败仅降级记日志，不影响响应。
//
// 典型用法（与 AuthzMiddleware 归一化对称）：
//
//	ginmw.AuditMiddleware(
//	    ginmw.AuditWithObjectResolver(authz.DefaultHTTPObject),
//	    ginmw.AuditWithActionResolver(authz.DefaultHTTPAction),
//	)
func AuditMiddleware(opts ...AuditOption) gin.HandlerFunc {
	cfg := &auditOptions{}
	for _, opt := range opts {
		opt(cfg)
	}
	auditor := cfg.auditor
	if auditor == nil {
		auditor = audit.GetAuditor()
	}
	rec := cfg.metricsRecorder
	if rec == nil {
		rec = metrics.NopRecorder()
	}
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		subject, tenant := subjectTenant(c, cfg)
		object, action := objectAction(c, cfg)
		result := audit.ResultAllow
		switch {
		case c.Writer.Status() == 401 || c.Writer.Status() == 403:
			result = audit.ResultDeny
		case c.Writer.Status() >= 500:
			result = audit.ResultError
		}
		ev := audit.AuditEvent{
			Time:     start,
			Subject:  subject,
			TenantID: tenant,
			Object:   object,
			Action:   action,
			Result:   result,
			Meta: map[string]any{
				"request_id": contextx.RequestIDFromContext(c.Request.Context()),
				"trace_id":   contextx.TraceIDFromContext(c.Request.Context()),
				"status":     c.Writer.Status(),
				"client_ip":  c.ClientIP(),
			},
		}
		if len(c.Errors) > 0 {
			ev.Result = audit.ResultError
			ev.Error = c.Errors.ByType(gin.ErrorTypePrivate).String()
		}
		recordSafely(auditor, c.Request.Context(), ev)
		// M8 指标埋点：与审计同源，复用 object/action/result 维度，旁路不阻断。
		emitMetricsSafely(rec, c.Request.Context(), metrics.Event{Object: object, Action: action, Result: string(ev.Result), Error: ev.Error}, metrics.TransportHTTP, time.Since(start).Seconds())
	}
}

// emitMetricsSafely 指标旁路记录：Recorder panic 仅降级记日志，绝不影响响应
// （D5：与 auditor 的 recordSafely 同一纪律，此前 metrics.Record 未被保护）。
func emitMetricsSafely(rec metrics.Recorder, ctx context.Context, ev metrics.Event, transport metrics.Transport, seconds float64) {
	if rec == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.GetLogger().Error(ctx, "metrics recorder panicked", "panic", r)
		}
	}()
	rec.Record(ctx, ev, transport, seconds)
}

func subjectTenant(c *gin.Context, cfg *auditOptions) (string, string) {
	if cfg.subjectResolver != nil {
		return cfg.subjectResolver(c), ""
	}
	if cl := authn.AuthClaimsFromContext(c.Request.Context()); cl != nil {
		return cl.Subject, cl.TenantID
	}
	return "", ""
}

func objectAction(c *gin.Context, cfg *auditOptions) (string, string) {
	object, action := c.Request.URL.Path, c.Request.Method
	if cfg.objectResolver != nil {
		object = cfg.objectResolver(c.Request.URL.Path)
	}
	if cfg.actionResolver != nil {
		action = cfg.actionResolver(c.Request.Method)
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
