package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// tenantFieldIndex 的 tag 解析优先级
//
// 补测动机：`splitTag`/`cutPrefix` 两个辅助函数此前 0% 覆盖——它们只在
// **gorm tag 分支**可达，而既有测试实体（tenantEntity）走的是 json tag 与
// 字段名推断。gorm tag 是生产 gorm 后端的真实写法，其解析路径需独立钉住。
//
// 匹配优先级（tenantFieldIndex 注释）：gorm column tag > json tag > 字段名转 snake。
// ---------------------------------------------------------------------------

// gormTagEntity 用 gorm column tag 声明租户列。
type gormTagEntity struct {
	ID string
	// gorm tag 显式声明列名——覆盖字段名推断。
	TID string `gorm:"column:tenant_id"`
}

// jsonTagEntity 用 json tag 声明租户列。
type jsonTagEntity struct {
	ID  string
	TID string `json:"tenant_id"`
}

// nameOnlyEntity 无任何 tag，靠字段名 CamelCase → snake_case 推断。
type nameOnlyEntity struct {
	ID       string
	TenantID string // → tenant_id
}

// TestTenantFieldIndex_GormTag 覆盖 splitTag/cutPrefix 路径。
func TestTenantFieldIndex_GormTag(t *testing.T) {
	idx := tenantFieldIndex(reflect.TypeOf(gormTagEntity{}), "tenant_id")
	assert.GreaterOrEqual(t, idx, 0, "应通过 gorm column tag 命中 TID 字段")
}

// TestTenantFieldIndex_JsonTag json tag 路径。
func TestTenantFieldIndex_JsonTag(t *testing.T) {
	idx := tenantFieldIndex(reflect.TypeOf(jsonTagEntity{}), "tenant_id")
	assert.GreaterOrEqual(t, idx, 0, "应通过 json tag 命中 TID 字段")
}

// TestTenantFieldIndex_NameInference 无 tag 时靠字段名推断。
func TestTenantFieldIndex_NameInference(t *testing.T) {
	idx := tenantFieldIndex(reflect.TypeOf(nameOnlyEntity{}), "tenant_id")
	assert.GreaterOrEqual(t, idx, 0, "应由 TenantID → tenant_id 推断命中")
}

// TestTenantFieldIndex_NotFound 无匹配列返回 -1。
func TestTenantFieldIndex_NotFound(t *testing.T) {
	idx := tenantFieldIndex(reflect.TypeOf(nameOnlyEntity{}), "no_such_column")
	assert.Equal(t, -1, idx)
}

// TestSplitTagAndCutPrefix 直接锁定两个辅助函数的行为（含多段 tag 与无前缀）。
func TestSplitTagAndCutPrefix(t *testing.T) {
	parts := splitTag("column:tenant_id;type:varchar(64);not null")
	assert.Equal(t, []string{"column:tenant_id", "type:varchar(64)", "not null"}, parts)

	// 带前缀命中。
	v, ok := cutPrefix("column:tenant_id", "column:")
	assert.True(t, ok)
	assert.Equal(t, "tenant_id", v)

	// 无前缀不命中，且原样返回。
	v, ok = cutPrefix("type:varchar", "column:")
	assert.False(t, ok)
	assert.Equal(t, "type:varchar", v)
}

// TestInjectWriteTenant_GormTagEntity 端到端：gorm tag 实体经写路径注入。
// 这同时覆盖了 tenantFieldIndex 的 gorm 分支在真实调用链中的可达性。
func TestInjectWriteTenant_GormTagEntity(t *testing.T) {
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	ctx := contextx.WithTenantID(context.Background(), "t-gorm")

	e := &gormTagEntity{ID: "1", TID: "t-evil"}
	injectWriteTenant(ctx, e)
	assert.Equal(t, "t-gorm", e.TID, "gorm tag 声明的租户列应被注入覆盖")

	// json tag 实体同理。
	je := &jsonTagEntity{ID: "2", TID: "t-evil"}
	injectWriteTenant(ctx, je)
	assert.Equal(t, "t-gorm", je.TID, "json tag 声明的租户列应被注入覆盖")

	// 字段名推断实体。
	ne := &nameOnlyEntity{ID: "3", TenantID: "t-evil"}
	injectWriteTenant(ctx, ne)
	assert.Equal(t, "t-gorm", ne.TenantID, "字段名推断的租户列应被注入覆盖")
}

// TestInjectWriteTenant_NonStringFieldSkipped 字段类型非 string 时静默跳过
// （injectWriteTenant 只覆写 string 字段），不得 panic。
func TestInjectWriteTenant_NonStringFieldSkipped(t *testing.T) {
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	type wrongTypeEntity struct {
		ID       string
		TenantID int // 非 string：注入应跳过
	}
	ctx := contextx.WithTenantID(context.Background(), "t-1")
	e := &wrongTypeEntity{ID: "1", TenantID: 99}

	assert.NotPanics(t, func() { injectWriteTenant(ctx, e) })
	assert.Equal(t, 99, e.TenantID, "非 string 字段不应被覆写")
}

// TestInjectWriteTenant_NonPointerSkipped 非指针/非 struct 入参静默跳过。
func TestInjectWriteTenant_NonPointerSkipped(t *testing.T) {
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	ctx := contextx.WithTenantID(context.Background(), "t-1")
	assert.NotPanics(t, func() {
		injectWriteTenant(ctx, nameOnlyEntity{}) // 非指针
		injectWriteTenant(ctx, nil)              // nil
		injectWriteTenant(ctx, "a string")       // 非 struct
	})
}
