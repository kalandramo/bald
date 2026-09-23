package store

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/pkg/authn"
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/stretchr/testify/assert"
)

func TestMergeTenant(t *testing.T) {
	// 多租户需显式开启：注册 tenant_id 维度（业务真实用法）。
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	ctx := contextx.WithTenantID(context.Background(), "t-42")

	w := &Where{}
	mergeTenant(w, ctx)
	assert.Len(t, w.Filters, 1)
	assert.Equal(t, "tenant_id", w.Filters[0].GetField())
	assert.Equal(t, "t-42", w.Filters[0].GetValue())

	// 业务已手写同名 EQ 条件时，不重复注入。
	w2 := &Where{Filters: []*storev1.FilterCondition{Eq("tenant_id", "t-other")}}
	mergeTenant(w2, ctx)
	assert.Len(t, w2.Filters, 1, "业务已声明租户条件，不应重复注入")

	// 业务已写该列的非 EQ 条件（如 NEQ）时，也不应再叠加系统 EQ，
	// 避免 EQ + NEQ 叠加产生恒假/歧义（Issue 5）。
	w3 := &Where{Filters: []*storev1.FilterCondition{Ne("tenant_id", "t-other")}}
	mergeTenant(w3, ctx)
	assert.Len(t, w3.Filters, 1, "业务已写该列任意 Op，系统不应再注入 EQ")
}

func TestMergeDataScope(t *testing.T) {
	// 显式开启租户维度。
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	// 注册一个"仅本人"数据范围策略。
	RegisterDataScope(func(_ context.Context, c *authn.AuthClaims) []*storev1.FilterCondition {
		if c == nil {
			return nil
		}
		return []*storev1.FilterCondition{Eq("owner", c.Subject)}
	})
	t.Cleanup(func() { dataScopes = nil })

	ctx := authn.ContextWithAuthClaims(context.Background(), &authn.AuthClaims{Subject: "u-1", TenantID: "t-9"})
	w := &Where{}
	mergeTenant(w, ctx)
	mergeDataScope(w, ctx)

	// 租户隔离 + 数据范围两条条件都注入。
	fields := map[string]string{}
	for _, f := range w.Filters {
		fields[f.GetField()] = f.GetValue()
	}
	assert.Equal(t, "t-9", fields["tenant_id"])
	assert.Equal(t, "u-1", fields["owner"])
}

// TestUnregisterTenant 验证逆操作契约（T1 效应账本的对偶成员）：
// 注册→隔离生效；注销→维度消失；重复注销幂等。
func TestUnregisterTenant(t *testing.T) {
	ctx := contextx.WithTenantID(context.Background(), "t-42")

	RegisterTenant("tenant_id", DefaultTenantFunc)
	w := &Where{}
	mergeTenant(w, ctx)
	assert.Len(t, w.Filters, 1, "注册后租户条件应注入")
	assert.Equal(t, "tenant_id", w.Filters[0].GetField())

	UnregisterTenant("tenant_id")
	w2 := &Where{}
	mergeTenant(w2, ctx)
	assert.Empty(t, w2.Filters, "注销后不应再注入租户条件")

	UnregisterTenant("tenant_id") // 幂等：重复注销不 panic
	w3 := &Where{}
	mergeTenant(w3, ctx)
	assert.Empty(t, w3.Filters)
}

// TestWhere_T_PreservesExpr 锁定 Where.T 必须保留 Expr（边界 2 修复）：
// T() 构造副本时曾只复制 Sorting/Offset/Limit/Filters，静默丢弃业务布尔树——
// 一旦业务用 where.T(ctx) 显式隔离，OR 树会被吞掉。修复后 Expr 应原样保留。
func TestWhere_T_PreservesExpr(t *testing.T) {
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	ctx := contextx.WithTenantID(context.Background(), "t-42")
	orTree := Or([]*storev1.FilterCondition{
		Eq("name", "alice"),
		Eq("name", "bob"),
	})
	w := &Where{Expr: orTree, Sorting: []*storev1.Sorting{Sort("age")}}

	got := w.T(ctx)

	// Expr 必须保留（原缺陷：丢失 → 业务 OR 树被静默丢弃）。
	assert.Same(t, orTree, got.Expr, "Where.T 必须保留 Expr（业务布尔树）")
	// 租户条件注入到 Filters，与 Expr 按 AND 连接。
	assert.Len(t, got.Filters, 1)
	assert.Equal(t, "tenant_id", got.Filters[0].GetField())
	// 原有字段仍保留。
	assert.Len(t, got.Sorting, 1)
	// 原对象不被修改（返回副本）。
	assert.Nil(t, w.Filters)
}

