// Package auditstore 把审计事件落库（gorm）——bald audit.Auditor 的存储桥接。
//
// 零后端耦合纪律的落地半边：核心 pkg/audit 只定义抽象，本模块提供真实
// 落库实现。落库失败或 panic 仅降级（记日志 + 可选 fallback 后端双写），
// 绝不向上游返回错误——审计旁路语义（与中间件 recordSafely 同一纪律）。
//
// 审计表刻意「全量记录」：不走读隔离语义（TenantID 自动过滤），TenantID
// 仅作为列存储，由审计查询方按需过滤。
package auditstore

import (
	"context"

	"gorm.io/gorm"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"
)

// AuditRecord 默认审计表模型，字段与 go-bald-admin 既有表对齐（防
// migrate 冲突）。表结构不同的业务用 WithRecordMapper 提供事件→记录
// 映射，映射返回值直接交给 gorm Create。
type AuditRecord struct {
	ID       uint   `gorm:"primaryKey;autoIncrement"` // 自增主键
	TenantID string `gorm:"index"`                    // 租户（来自 AuditEvent.TenantID）
	Time     int64  `gorm:"index"`                    // 事件时间（UnixNano）
	Subject  string // 操作主体（来自 AuditEvent.Subject）
	Object   string // 资源对象（来自 AuditEvent.Object）
	Action   string // 操作动作（来自 AuditEvent.Action）
	Result   string // allow/deny/error（来自 AuditEvent.Result）
	Error    string // 错误详情（空为成功）

	// 扩展列：从 AuditEvent.Meta 提取（拦截器注入的上下文）。
	Category  string `gorm:"index"` // 分类："operation"（拦截器链）/ "login"（登录动作）
	IPAddress string // 客户端 IP（Meta["client_ip"]）
	UserAgent string // 客户端 UA（Meta["user_agent"]）
	RequestID string // 全局请求 ID（Meta["request_id"]）
	TraceID   string // W3C 链路 ID（Meta["trace_id"]）
}

// Option 配置 StoreAuditor。
type Option func(*storeConfig)

type storeConfig struct {
	mapper   func(audit.AuditEvent) any
	fallback audit.Auditor
}

// WithRecordMapper 自定义事件→落库记录映射（业务表结构与默认 AuditRecord
// 不同时使用）。返回值直接传 gorm Create，须为业务已迁移的表模型指针。
func WithRecordMapper(fn func(audit.AuditEvent) any) Option {
	return func(c *storeConfig) { c.mapper = fn }
}

// WithFallback 设置落库失败时的降级后端（双写语义：落库失败的事件仍进
// fallback，不丢审计痕迹）。缺省 LoggerAuditor（结构化日志）；传
// audit.NopAuditor() 关闭降级。
func WithFallback(a audit.Auditor) Option {
	return func(c *storeConfig) { c.fallback = a }
}

// StoreAuditor 把审计事件落库（gorm）。
type StoreAuditor struct {
	db       *gorm.DB
	mapper   func(audit.AuditEvent) any
	fallback audit.Auditor
}

// New 构造落库审计后端。db 为 nil 时 Record 全部走 fallback（旁路不 panic）。
// 表结构迁移由业务负责：db.AutoMigrate(store.DefaultModel())（或业务自定义表）。
func New(db *gorm.DB, opts ...Option) *StoreAuditor {
	cfg := storeConfig{
		mapper:   defaultRecord,
		fallback: audit.NewLoggerAuditor(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &StoreAuditor{db: db, mapper: cfg.mapper, fallback: cfg.fallback}
}

// DefaultModel 返回默认表模型，供业务 AutoMigrate。
func DefaultModel() any { return &AuditRecord{} }

// Record 实现 audit.Auditor：事件写库；失败/panic 降级 fallback 双写。
func (a *StoreAuditor) Record(ctx context.Context, ev audit.AuditEvent) {
	defer func() {
		if r := recover(); r != nil {
			log.Warn(ctx, "audit-store: record panicked", "panic", r)
		}
	}()
	if a.db == nil {
		a.recordFallback(ctx, ev)
		return
	}
	if err := a.db.WithContext(ctx).Create(a.mapper(ev)).Error; err != nil {
		log.Warn(ctx, "audit-store: create failed", "error", err.Error())
		a.recordFallback(ctx, ev)
	}
}

// recordFallback 降级双写（fallback 为 nil 时静默丢弃）。
func (a *StoreAuditor) recordFallback(ctx context.Context, ev audit.AuditEvent) {
	if a.fallback != nil {
		a.fallback.Record(ctx, ev)
	}
}

// defaultRecord 从审计事件构造默认表记录（Meta 提取扩展字段，缺省兜底）。
func defaultRecord(ev audit.AuditEvent) any {
	return &AuditRecord{
		TenantID:  ev.TenantID,
		Time:      ev.Time.UnixNano(),
		Subject:   ev.Subject,
		Object:    ev.Object,
		Action:    ev.Action,
		Result:    string(ev.Result),
		Error:     ev.Error,
		Category:  metaString(ev.Meta, "category", "operation"),
		IPAddress: metaString(ev.Meta, "client_ip", ""),
		UserAgent: metaString(ev.Meta, "user_agent", ""),
		RequestID: metaString(ev.Meta, "request_id", ""),
		TraceID:   metaString(ev.Meta, "trace_id", ""),
	}
}

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

// compile-time 断言 StoreAuditor 实现 audit.Auditor。
var _ audit.Auditor = (*StoreAuditor)(nil)
