package model

import "time"

// Tenant 业务租户（T2，自 go-wind-admin sys_tenants 精简移植，见移植计划 §6 D2）。
// ID 即租户编码（如 "platform"、"t-acme"），与 bald P8 的 tenant_id 同一命名空间：
// 业务租户创建后即可作为 users/secrets 等业务表的隔离维度值使用。
//
// 刻意不设 TenantID 字段——本表是「租户即业务实体」的平台侧管理面：P8 的写注入
// （injectWriteTenant 对无 TenantID 字段实体静默跳过）与读过滤（查询不调 Where.T）
// 都天然不作用于它；全量读写即平台语义（源项目 PlatformTenantID=0 等价物）。
type Tenant struct {
	ID        string `gorm:"primaryKey"` // 租户编码（= P8 tenant_id）
	Name      string // 展示名
	Status    string // "ON"/"OFF"/"FREEZE"（源 sys_tenants.status 精简）
	Remark    string
	CreatedAt time.Time
	UpdatedAt time.Time
}
