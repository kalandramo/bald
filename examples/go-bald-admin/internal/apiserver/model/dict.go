package model

import "time"

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
