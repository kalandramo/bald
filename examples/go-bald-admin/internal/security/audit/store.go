package audit

import (
	"context"

	"gorm.io/gorm"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"

	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
)

// StoreAuditor 把审计事件落库（GORM AuditRecord 表），与 LoggerAuditor 同属「真实后端」
// 而非 fake/stub，符合设计文档 §0。落库失败或 panic 仅降级记日志，绝不向上游返回——
// 复用 LoggerAuditor 同时把事件写结构化日志，保证落库异常时仍有可见副作用。
//
// 注意：审计表刻意「全量记录」，不走现有 pkg/store 的 TenantID 自动读过滤（那是读隔离语义，
// 审计是写全量留痕）；TenantID 仅作为列存储，由审计查询方按需过滤。
type StoreAuditor struct {
	DB *gorm.DB
	// fallback 落库失败时仍写日志（默认 LoggerAuditor，可置 nil 关闭）。
	fallback audit.Auditor
}

// NewStore 构造落库审计后端；fallback 默认 LoggerAuditor（双写），传 nil 则仅落库。
func NewStore(db *gorm.DB) *StoreAuditor {
	return &StoreAuditor{DB: db, fallback: New()}
}

// Record 实现 audit.Auditor：把事件写库，失败 panic 降级。
func (a *StoreAuditor) Record(ctx context.Context, ev audit.AuditEvent) {
	defer func() {
		if r := recover(); r != nil {
			log.GetLogger().Warn(ctx, "StoreAuditor panic recovered", "error", r)
		}
	}()
	if a.DB == nil {
		if a.fallback != nil {
			a.fallback.Record(ctx, ev)
		}
		return
	}
	rec := &authmodel.AuditRecord{
		TenantID: ev.TenantID,
		Time:     ev.Time.UnixNano(),
		Subject:  ev.Subject,
		Object:   ev.Object,
		Action:   ev.Action,
		Result:   string(ev.Result),
		Error:    ev.Error,
	}
	// T6 增强：从 Meta 提取扩展字段（客户端信息/关联 ID/分类），缺省零值。
	// Category 由事件来源标记：拦截器链未标记时按 "operation" 兜底。
	rec.Category = metaString(ev.Meta, "category", "operation")
	rec.IPAddress = metaString(ev.Meta, "client_ip", "")
	rec.UserAgent = metaString(ev.Meta, "user_agent", "")
	rec.RequestID = metaString(ev.Meta, "request_id", "")
	rec.TraceID = metaString(ev.Meta, "trace_id", "")
	if err := a.DB.WithContext(ctx).Create(rec).Error; err != nil {
		log.GetLogger().Warn(ctx, "StoreAuditor create failed", "error", err.Error())
		if a.fallback != nil {
			a.fallback.Record(ctx, ev)
		}
	}
}

// compile-time 断言 StoreAuditor 实现 audit.Auditor。
var _ audit.Auditor = (*StoreAuditor)(nil)

// metaString 从 Meta 取字符串值；缺键/类型不符返回 def。
func metaString(meta map[string]any, key, def string) string {
	if meta == nil {
		return def
	}
	if v, ok := meta[key].(string); ok && v != "" {
		return v
	}
	return def
}
