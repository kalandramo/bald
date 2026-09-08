package model

// AuditRecord 审计事件落库实体（M9 延伸：审计后端落库）。
// 与 User/Secret 不同，审计表刻意「全量记录」——不走现有 pkg/store 的 TenantID 自动过滤
// （那是读隔离语义，审计是写全量留痕）；TenantID 仅作为列存储，由审计查询方按需过滤。
type AuditRecord struct {
	ID       uint   `gorm:"primaryKey;autoIncrement"` // 自增主键
	TenantID string `gorm:"index"`                    // 租户（来自 AuditEvent.TenantID）
	Time     int64  `gorm:"index"`                    // 事件时间（UnixNano）
	Subject  string // 操作主体（来自 AuditEvent.Subject）
	Object   string // 资源对象（来自 AuditEvent.Object）
	Action   string // 操作动作（来自 AuditEvent.Action）
	Result   string // allow/deny/error（来自 AuditEvent.Result）
	Error    string // 错误详情（来自 AuditEvent.Error，空为成功）

	// T6 审计增强（自源 audit/service/v1 的 Operation/LoginAuditLog 精简，
	// 单表 + Category 分类而非双表；Success 由 Result 推导不冗余存储）。
	Category  string `gorm:"index"` // 分类："operation"（拦截器链）/ "login"（登录动作）
	IPAddress string // 客户端 IP（Meta["client_ip"]，源 ip_address）
	UserAgent string // 客户端 UA（Meta["user_agent"]，源 device_info 精简）
	RequestID string // 全局请求 ID（Meta["request_id"]，关联网关日志）
	TraceID   string // W3C 链路 ID（Meta["trace_id"]）
}
