package store

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/stretchr/testify/assert"
)

// tenantEntity 用于写路径多租户注入测试，含 TenantID 字段（CamelCase → snake tenant_id）。
type tenantEntity struct {
	ID       string
	Name     string
	TenantID string
}

func TestInjectWriteTenant(t *testing.T) {
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { RegisterTenant("tenant_id", nil) })

	ctx := contextx.WithTenantID(context.Background(), "t-7")

	e := &tenantEntity{ID: "1", Name: "x"}
	injectWriteTenant(ctx, e)
	assert.Equal(t, "t-7", e.TenantID, "未显式设租户时应自动注入 ctx 租户")

	// 越权设置租户值 → 被 ctx 真实租户覆盖（防改归属）。
	rogue := &tenantEntity{ID: "2", Name: "y", TenantID: "t-evil"}
	injectWriteTenant(ctx, rogue)
	assert.Equal(t, "t-7", rogue.TenantID, "越权租户值应被 ctx 租户覆盖")

	// 无租户 ctx → 不注入，保留实体原值（含零值）。
	noTenant := &tenantEntity{ID: "3", Name: "z", TenantID: "t-keep"}
	injectWriteTenant(context.Background(), noTenant)
	assert.Equal(t, "t-keep", noTenant.TenantID, "无租户 ctx 不应改动实体租户")

	// 非租户实体（无 TenantID 字段）→ 静默跳过，不 panic。
	type plain struct{ ID string }
	injectWriteTenant(ctx, &plain{ID: "4"})
}
