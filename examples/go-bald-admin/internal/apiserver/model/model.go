// Package model 是 go-bald-admin 的 GORM 实体（存储层）。
//
// 仅承载「表结构 + 列映射」，不含业务逻辑；多租户隔离由 bald core 的
// pkg/store 在查询时自动注入 TenantID 过滤（M2 起生效）。字段名经
// baldgorm.toColumn 默认 snake_case 映射为列名（ID->id, TenantID->tenant_id）。
package model

import "strings"

// User 系统用户。Roles 以逗号分隔存储角色名（MVP 简化，避免独立关联表）。
type User struct {
	ID           string `gorm:"primaryKey"` // 用户 ID（如 u-admin）
	Username     string `gorm:"uniqueIndex"` // 登录名
	PasswordHash string // bcrypt 哈希（M3 起，MVP 明文阶段已废弃）
	TenantID     string `gorm:"index"`
	Roles        string // 逗号分隔角色名，如 "admin" 或 "viewer"
}

// Role 角色到权限点的映射。Perms 以逗号分隔存储 "object:action" 权限点。
type Role struct {
	ID    string `gorm:"primaryKey"` // 角色名（如 admin）
	Perms string // 逗号分隔权限点，如 "secret:get,secret:delete"
}

// Secret 受限资源（M6.3 起落库，替换 handler 硬编码返回）。多租户隔离由 bald core
// pkg/store 在查询时自动注入 TenantID 过滤（同 User/Role）。
type Secret struct {
	ID      string `gorm:"primaryKey"` // 机密 ID（如 s-db-pwd）
	Name    string // 展示名（如 "数据库口令"）
	Content string // 机密内容（明文存储于演示库；生产应加密/KMS）
	TenantID string `gorm:"index"`
}

// RolesList 解析 Roles 字段为角色名切片。
func (u User) RolesList() []string {
	return splitCSV(u.Roles)
}

// AuditRecord 审计事件落库实体（M9 延伸：审计后端落库）。
// 与 User/Secret 不同，审计表刻意「全量记录」——不走现有 pkg/store 的 TenantID 自动过滤
// （那是读隔离语义，审计是写全量留痕）；TenantID 仅作为列存储，由审计查询方按需过滤。
type AuditRecord struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"` // 自增主键
	TenantID  string `gorm:"index"`                     // 租户（来自 AuditEvent.TenantID）
	Time      int64  `gorm:"index"`                     // 事件时间（UnixNano）
	Subject   string // 操作主体（来自 AuditEvent.Subject）
	Object    string // 资源对象（来自 AuditEvent.Object）
	Action    string // 操作动作（来自 AuditEvent.Action）
	Result    string // allow/deny/error（来自 AuditEvent.Result）
	Error     string // 错误详情（来自 AuditEvent.Error，空为成功）
}

// PermsList 解析 Perms 字段为权限点切片。
func (r Role) PermsList() []string {
	return splitCSV(r.Perms)
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
