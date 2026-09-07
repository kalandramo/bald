// Package model 是 go-bald-admin 的 GORM 实体（存储层）。
//
// 仅承载「表结构 + 列映射」，不含业务逻辑；多租户隔离由 bald core 的
// pkg/store 在查询时自动注入 TenantID 过滤（M2 起生效）。字段名经
// baldgorm.toColumn 默认 snake_case 映射为列名（ID->id, TenantID->tenant_id）。
package model

import (
	"strings"
	"time"
)

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

// Role 角色到权限点的映射。Perms 以逗号分隔存储 "object:action" 权限点。
type Role struct {
	ID    string `gorm:"primaryKey"` // 角色名（如 admin）
	Perms string // 逗号分隔权限点，如 "secret:get,secret:delete"
}

// Secret 受限资源（M6.3 起落库，替换 handler 硬编码返回）。多租户隔离由 bald core
// pkg/store 在查询时自动注入 TenantID 过滤（同 User/Role）。
type Secret struct {
	ID       string `gorm:"primaryKey"` // 机密 ID（如 s-db-pwd）
	Name     string // 展示名（如 "数据库口令"）
	Content  string // 机密内容（明文存储于演示库；生产应加密/KMS）
	TenantID string `gorm:"index"`
}

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

// DictType 字典类型（T4，自 go-wind-admin sys_dict_types 精简移植）。ID 即类型
// 编码（源 type_code，immutable 语义——业务键做主键，同 Menu/Permission 范式）。
// 带 TenantID 字段：字典是租户级业务数据（源 mixin TenantID），P8 自动隔离；
// Cache-Aside 键亦含租户维度（dict biz）。
type DictType struct {
	ID        string `gorm:"primaryKey"` // 类型编码（如 "gender"）
	TenantID  string `gorm:"index"`
	TypeName  string // 显示名称（源 type_name）
	SortOrder int32  // 展示顺序（源 sort_order）
	Enabled   bool   // 启用（源 IsEnabled mixin）
	Remark    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DictEntry 字典项（T4，自 go-wind-admin sys_dict_entries 精简移植）。主键 =
// "<type_code>:<entry_value>" 业务键（同 RolePolicy 范式：Create 冲突即条目重复，
// 源「同租户同类型 entry_value 唯一」约束的等价实现——type_code/value 不可变，
// Update 仅改展示属性）。Label 内联源 sys_dict_entry_i18n 的 zh 条目（i18n 表按
// §2.2 多语言后续迭代不移植）；Numeric 对应源 numeric_value 可空数值。
type DictEntry struct {
	ID        string `gorm:"primaryKey"` // 业务键 "<type_code>:<entry_value>"
	TenantID  string `gorm:"index"`
	TypeCode  string `gorm:"index"` // 所属类型编码（源 type_id FK 精简为编码引用）
	Value     string // 条目实际值（源 entry_value）
	Label     string // 显示标签（源 i18n zh 内联）
	Numeric   *int32 // 数值型值（可空，源 numeric_value）
	SortOrder int32
	Enabled   bool
	Remark    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// File 文件元数据（T5，自 go-wind-admin files 表精简移植，语义对齐源
// storage/service/v1 的 File/recordFile）。ID = 保存文件名去扩展名的 uuid
// （32 位无横线，源 FileGuid 范式）；SaveFileName 是 MinIO 对象键（含目录与
// 扩展名）。带 TenantID：文件是租户级业务数据（源 mixin TenantID），P8 自动
// 隔离。ContentHash 是上传内容的 SHA256 hex（源 ContentHash）；对象按内容
// 类型分桶（BucketName 实际落桶），LinkUrl 预签名后续迭代留空。
type File struct {
	ID            string `gorm:"primaryKey"` // uuid32（保存文件名去扩展名）
	TenantID      string `gorm:"index"`
	Provider      string // 存储提供者（"minio"）
	BucketName    string // 实际落桶（按内容类型分桶）
	SaveFileName  string // 对象键（含目录与扩展名）
	FileDirectory string // 业务目录
	FileName      string // 原始文件名
	Extension     string // 扩展名（含点，小写）
	ContentHash   string `gorm:"index"` // SHA256 hex
	Size          int64  // 字节数
	LinkUrl       string // 访问 URL（预签名后续迭代）
	MimeType      string // 嗅探出的内容类型
	CreatedBy     string // 上传者（UserID）
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// RolesList 解析 Roles 字段为角色名切片。
func (u User) RolesList() []string {
	return splitCSV(u.Roles)
}

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
