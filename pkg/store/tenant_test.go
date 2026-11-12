package store

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeTenant(t *testing.T) {
	// 多租户需显式开启：注册 tenant_id 维度（业务真实用法）。
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	ctx := contextx.WithTenantID(context.Background(), "t-42")

	w := &Where{}
	require.NoError(t, mergeTenant(w, ctx))
	require.Len(t, w.Filters, 1)
	assert.Equal(t, "tenant_id", w.Filters[0].GetField())
	assert.Equal(t, "t-42", w.Filters[0].GetValue())

	// 业务已手写「同字段 + EQ + 同值」才算自行承担隔离语义，不重复注入。
	w2 := &Where{Filters: []*storev1.FilterCondition{Eq("tenant_id", "t-42")}}
	require.NoError(t, mergeTenant(w2, ctx))
	assert.Len(t, w2.Filters, 1, "同字段同值 EQ 应去重")

	// 异值 EQ 必须仍注入系统条件——AND 叠加只会更严格，绝不放宽。
	w3 := &Where{Filters: []*storev1.FilterCondition{Eq("tenant_id", "t-other")}}
	require.NoError(t, mergeTenant(w3, ctx))
	assert.Len(t, w3.Filters, 2, "异值 EQ 不得顶掉系统隔离条件")
}

// TestMergeTenant_NonEQCannotBypass —— 安全回归：非 EQ 操作符不得顶掉隔离条件。
//
// 旧实现按**字段名**去重（`if f.GetField() == tc.GetField()`），于是上游只要传一条
// `NEQ tenant_id='none'`，系统注入的 `Eq(tenant_id, ctx租户)` 就被丢弃，
// WHERE 变成 `tenant_id <> 'none'` —— 全租户可读。这条路径**不需要**任何身份缺陷，
// 合法身份也能触发，是本次审计里最隐蔽的一条。
func TestMergeTenant_NonEQCannotBypass(t *testing.T) {
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	ctx := contextx.WithTenantID(context.Background(), "t-42")

	for _, c := range []struct {
		name string
		cond *storev1.FilterCondition
	}{
		{"NEQ", Ne("tenant_id", "t-other")},
		{"IN", In("tenant_id", "t-1", "t-2")},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := &Where{Filters: []*storev1.FilterCondition{c.cond}}
			require.NoError(t, mergeTenant(w, ctx))
			require.Len(t, w.Filters, 2, "%s 必须与系统条件叠加，不得顶掉隔离", c.name)

			found := false
			for _, f := range w.Filters {
				if f.GetField() == "tenant_id" &&
					f.GetOp() == storev1.Operator_EQ &&
					f.GetValue() == "t-42" {
					found = true
				}
			}
			assert.True(t, found, "系统 EQ 必须仍存在（%s 绕过路径已关闭）", c.name)
		})
	}
}

// TestMergeTenant_MissingTenant —— 已注册维度但 ctx 取不到值 ⇒ ErrMissingTenant。
//
// 这是「身份不完整」而不是「不需要隔离」：多租户应用注册了维度，
// 请求却拿不到租户值（token 缺 tid claim、异步任务裸 ctx），
// 旧实现静默跳过 ⇒ 零过滤 ⇒ 全租户可读、可写。
func TestMergeTenant_MissingTenant(t *testing.T) {
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	w := &Where{}
	err := mergeTenant(w, context.Background())
	require.ErrorIs(t, err, ErrMissingTenant)
	assert.Empty(t, w.Filters, "拒绝时不得留下部分注入")
}

// TestMergeTenant_NoDimensionRegistered —— 非多租户应用：不隔离，也不报错。
//
// 必须与上一条区分：`tenantExtractors` 为空是合法的"这不是多租户应用"，
// 不是身份缺陷。把两者混为一谈正是旧实现 fail-open 的根源。
func TestMergeTenant_NoDimensionRegistered(t *testing.T) {
	UnregisterTenant("tenant_id")
	UnregisterTenant("org_id")

	w := &Where{}
	require.NoError(t, mergeTenant(w, context.Background()))
	assert.Empty(t, w.Filters)
}

// TestMergeTenant_MultiDimensionAnyMissing —— 多维度判定必须逐维度，不得聚合。
//
// 「至少一维取到值就放行」会让缺 org_id 的查询变成**部分越权**：
// 结果集看着正常（tenant 过滤生效），但 org 维度完全没约束，比全放行更难发现。
func TestMergeTenant_MultiDimensionAnyMissing(t *testing.T) {
	RegisterTenant("tenant_id", DefaultTenantFunc)
	RegisterTenant("org_id", func(context.Context) (string, bool) { return "", false })
	t.Cleanup(func() {
		UnregisterTenant("tenant_id")
		UnregisterTenant("org_id")
	})

	ctx := contextx.WithTenantID(context.Background(), "t-42")
	err := mergeTenant(&Where{}, ctx)
	require.ErrorIs(t, err, ErrMissingTenant, "任一已注册维度缺失即身份不完整")
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
	require.NoError(t, mergeTenant(w, ctx))
	mergeDataScope(w, ctx)

	// 租户隔离 + 数据范围两条条件都注入。
	fields := map[string]string{}
	for _, f := range w.Filters {
		fields[f.GetField()] = f.GetValue()
	}
	assert.Equal(t, "t-9", fields["tenant_id"])
	assert.Equal(t, "u-1", fields["owner"])
}

// TestUnregisterTenant 验证逆操作契约（效应账本的对偶成员）：
// 注册→隔离生效；注销→维度消失；重复注销幂等。
func TestUnregisterTenant(t *testing.T) {
	ctx := contextx.WithTenantID(context.Background(), "t-42")

	RegisterTenant("tenant_id", DefaultTenantFunc)
	w := &Where{}
	require.NoError(t, mergeTenant(w, ctx))
	assert.Len(t, w.Filters, 1, "注册后租户条件应注入")
	assert.Equal(t, "tenant_id", w.Filters[0].GetField())

	UnregisterTenant("tenant_id")
	w2 := &Where{}
	require.NoError(t, mergeTenant(w2, ctx))
	assert.Empty(t, w2.Filters, "注销后不应再注入租户条件")

	UnregisterTenant("tenant_id") // 幂等：重复注销不 panic
	w3 := &Where{}
	require.NoError(t, mergeTenant(w3, ctx))
	assert.Empty(t, w3.Filters)
}

// TestWhere_T_PreservesExpr 锁定 Where.T 必须保留 Expr：T() 构造副本时若只
// 复制 Sorting/Offset/Limit/Filters，会静默丢弃业务布尔树——一旦业务用
// where.T(ctx) 显式隔离，OR 树会被吞掉。
func TestWhere_T_PreservesExpr(t *testing.T) {
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	ctx := contextx.WithTenantID(context.Background(), "t-42")
	orTree := Or([]*storev1.FilterCondition{
		Eq("name", "alice"),
		Eq("name", "bob"),
	})
	w := &Where{Expr: orTree, Sorting: []*storev1.Sorting{Sort("age")}}

	got, err := w.T(ctx)
	require.NoError(t, err)

	// Expr 必须保留（丢失 → 业务 OR 树被静默丢弃）。
	assert.Same(t, orTree, got.Expr, "Where.T 必须保留 Expr（业务布尔树）")
	// 租户条件注入到 Filters，与 Expr 按 AND 连接。
	assert.Len(t, got.Filters, 1)
	assert.Equal(t, "tenant_id", got.Filters[0].GetField())
	// 原有字段仍保留。
	assert.Len(t, got.Sorting, 1)
	// 原对象不被修改（返回副本）。
	assert.Nil(t, w.Filters)
}

// TestWhere_T_MissingTenant —— Where.T 在身份缺租户时同样拒绝，不再静默跳过。
func TestWhere_T_MissingTenant(t *testing.T) {
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	got, err := (&Where{}).T(context.Background())
	require.ErrorIs(t, err, ErrMissingTenant)
	assert.Nil(t, got)
}
