package model

import "time"

// User 系统用户。Roles 以逗号分隔存储角色名（MVP 简化，避免独立关联表）。
type User struct {
	ID           string `gorm:"primaryKey"`  // 用户 ID（如 u-admin）
	Username     string `gorm:"uniqueIndex"` // 登录名
	PasswordHash string // bcrypt 哈希（M3 起，MVP 明文阶段已废弃）
	TenantID     string `gorm:"index"`
	Roles        string // 逗号分隔角色名，如 "admin" 或 "viewer"
	// 时间戳由 GORM 约定自动维护（CreatedAt 插入、UpdatedAt 每次更新）；T2 起对外暴露。
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RolesList 解析 Roles 字段为角色名切片。
func (u User) RolesList() []string {
	return splitCSV(u.Roles)
}
