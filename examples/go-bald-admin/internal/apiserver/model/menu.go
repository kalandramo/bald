package model

import "time"

// Menu 菜单节点（T3，自 go-wind-admin sys_menus 精简移植）。自引用树：ParentID
// 空串 = 根节点（源 parent_id=0 语义——uint32 零值做不了"未设置"，string 空
// 串天然区分）。本表是平台侧管理面（无 TenantID 字段，P8 不作用于它，全量读写
// 即平台语义，同 Tenant 表）。
type Menu struct {
	ID        string `gorm:"primaryKey"` // 菜单 ID（语义编码，如 "menu-system"）
	ParentID  string `gorm:"index"`      // 父节点 ID（空 = 根）
	Type      string // "CATALOG"/"MENU"/"BUTTON"（源 menu.type 精简）
	Name      string // 路由名
	Path      string // 路由路径（BUTTON 时存数据操作名）
	Component string // 前端组件
	Title     string // 展示标题（源 meta.title）
	Icon      string // 图标（源 meta.icon）
	Order     int32  // 展示顺序（源 meta.order，越小越前）
	Status    string // "ON"/"OFF"（源 SwitchStatus 精简）
	Remark    string
	CreatedAt time.Time
	UpdatedAt time.Time

	// Children 是树构建的内存嵌套（ListMenus 树形态）；非列，GORM 忽略。
	Children []*Menu `gorm:"-"`
}
