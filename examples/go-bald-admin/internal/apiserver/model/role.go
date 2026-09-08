package model

import "time"

// Role 角色到权限点的映射。Perms 以逗号分隔存储 "object:action" 权限点。
type Role struct {
	ID    string `gorm:"primaryKey"` // 角色名（如 admin）
	Perms string // 逗号分隔权限点，如 "secret:get,secret:delete"
}

// Permission 权限点注册表（T3，源 sys_permissions + sys_permission_menus 精简）。
// ID 即权限码（P9 归一化 "object:action"，与 Role.Perms 同命名空间）；MenuIDs
// 内联权限点→菜单可见性关联（源独立关联表，CSV 精简——同 User.Roles 简化范式）。
type Permission struct {
	ID        string `gorm:"primaryKey"` // 权限码（如 "tenant:list"）
	Name      string // 权限名称
	MenuIDs   string // 逗号分隔菜单 ID（源 permission_menu 关联内联）
	Remark    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RolePolicy 角色策略行（T3，casbin p 行的数据化持久层）。替代 M6.1 静态
// rbac_policy.csv（D3 策略数据化装载）：装载时全表读出拼 csv 注入 contrib
// casbin，行格式 `p, <role>, <object>, <action>`；g 行（subject→角色）由
// User.Roles 装载，不落本表。主键 = "role:object:action" 业务键（仓库统一
// string 主键范式，Create 冲突即重复策略天然防重）。源 sys_role_permissions
// 的 effect/priority 未消费（源项目同样未用），精简掉。
type RolePolicy struct {
	ID     string `gorm:"primaryKey"` // 业务键 "role:object:action"
	Role   string `gorm:"index"`      // 角色（casbin subject）
	Object string // P9 归一化资源名（如 "tenant"）
	Action string // 动作（get/list/write/delete）
}

// PermsList 解析 Perms 字段为权限点切片。
func (r Role) PermsList() []string {
	return splitCSV(r.Perms)
}
