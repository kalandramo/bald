package model

// Secret 受限资源（M6.3 起落库，替换 handler 硬编码返回）。多租户隔离由 bald core
// pkg/store 在查询时自动注入 TenantID 过滤（同 User/Role）。
type Secret struct {
	ID       string `gorm:"primaryKey"` // 机密 ID（如 s-db-pwd）
	Name     string // 展示名（如 "数据库口令"）
	Content  string // 机密内容（明文存储于演示库；生产应加密/KMS）
	TenantID string `gorm:"index"`
}
