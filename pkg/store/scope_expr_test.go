package store

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 布尔树版数据范围（RegisterDataScopeExpr / mergeDataScopeExpr）
//
// 补测动机：`RegisterDataScopeExpr` 此前 0% 覆盖、`mergeDataScopeExpr` 仅 50%
// ——而这是 **crudbridge 五级数据范围（SELF/USER/UNIT/ALL/NONE）的下沉路径**
// （crudbridge.RegisterDataScope → store.RegisterDataScopeExpr）。它的语义
// 错误会静默导致越权可见，值得独立钉住。
// ---------------------------------------------------------------------------

// resetScopes 清空已注册的数据范围策略（全局状态，测试间须隔离）。
func resetScopes(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		scopeMu.Lock()
		dataScopes = nil
		dataScopeExpr = nil
		scopeMu.Unlock()
	})
}

// TestMergeDataScopeExpr_InjectedIntoWhere 注册的布尔树应并入 Where.Expr。
func TestMergeDataScopeExpr_InjectedIntoWhere(t *testing.T) {
	resetScopes(t)

	RegisterDataScopeExpr(func(_ context.Context, _ *authn.AuthClaims) *storev1.FilterExpr {
		return Or([]*storev1.FilterCondition{Eq("owner", "u-1"), Eq("owner", "u-2")})
	})

	ctx := authn.ContextWithAuthClaims(context.Background(), &authn.AuthClaims{Subject: "u-1"})
	w := &Where{}
	mergeDataScopeExpr(w, ctx)

	require.NotNil(t, w.Expr, "布尔树版数据范围应并入 Where.Expr")
	assert.Equal(t, storev1.ExprType_OR, w.Expr.GetType())
	assert.Len(t, w.Expr.GetConditions(), 2)
}

// TestMergeDataScopeExpr_AndsWithBusinessExpr 数据范围树与业务树按 AND 组合
// ——业务 OR 树不得被吞掉（边界 2 同族关注点）。
func TestMergeDataScopeExpr_AndsWithBusinessExpr(t *testing.T) {
	resetScopes(t)

	RegisterDataScopeExpr(func(_ context.Context, _ *authn.AuthClaims) *storev1.FilterExpr {
		return And([]*storev1.FilterCondition{Eq("dept_id", "d-1")})
	})

	bizTree := Or([]*storev1.FilterCondition{Eq("name", "alice"), Eq("name", "bob")})
	w := &Where{Expr: bizTree}
	mergeDataScopeExpr(w, context.Background())

	require.NotNil(t, w.Expr)
	assert.Equal(t, storev1.ExprType_AND, w.Expr.GetType(), "范围树与业务树应 AND 组合")
	groups := w.Expr.GetGroups()
	require.Len(t, groups, 2, "应含范围树与业务树两个子组")
	// 业务 OR 树必须原样保留（不被吞）。
	assert.Same(t, bizTree, groups[1], "业务布尔树应原样保留在 AND 组中")
}

// TestMergeDataScopeExpr_MultipleStrategiesAllAnded 多策略注册时全部并入
// （与扁平版一致：可多次注册，条件合并）。
func TestMergeDataScopeExpr_MultipleStrategiesAllAnded(t *testing.T) {
	resetScopes(t)

	RegisterDataScopeExpr(func(_ context.Context, _ *authn.AuthClaims) *storev1.FilterExpr {
		return And([]*storev1.FilterCondition{Eq("a", "1")})
	})
	RegisterDataScopeExpr(func(_ context.Context, _ *authn.AuthClaims) *storev1.FilterExpr {
		return And([]*storev1.FilterCondition{Eq("b", "2")})
	})

	w := &Where{}
	mergeDataScopeExpr(w, context.Background())

	require.NotNil(t, w.Expr)
	assert.Equal(t, storev1.ExprType_AND, w.Expr.GetType())
	assert.Len(t, w.Expr.GetGroups(), 2, "两个策略的树都应并入")
}

// TestMergeDataScopeExpr_NilReturnSkipped 策略返回 nil 时跳过（不得产生空节点）。
func TestMergeDataScopeExpr_NilReturnSkipped(t *testing.T) {
	resetScopes(t)

	RegisterDataScopeExpr(func(_ context.Context, _ *authn.AuthClaims) *storev1.FilterExpr {
		return nil // 如 ALL 范围放行
	})

	w := &Where{}
	mergeDataScopeExpr(w, context.Background())
	assert.Nil(t, w.Expr, "策略返回 nil 时不应产生任何 Expr")
}

// TestMergeDataScopeExpr_SingleStrategyNoRedundantAnd 单策略且无业务树时
// 直接采用该树（不包一层冗余 AND）。
func TestMergeDataScopeExpr_SingleStrategyNoRedundantAnd(t *testing.T) {
	resetScopes(t)

	single := And([]*storev1.FilterCondition{Eq("x", "1")})
	RegisterDataScopeExpr(func(_ context.Context, _ *authn.AuthClaims) *storev1.FilterExpr {
		return single
	})

	w := &Where{}
	mergeDataScopeExpr(w, context.Background())
	assert.Same(t, single, w.Expr, "单策略无业务树时应直接采用，不加冗余 AND 包装")
}

// TestMergeDataScopeExpr_NoRegistrationNoop 未注册时零影响。
func TestMergeDataScopeExpr_NoRegistrationNoop(t *testing.T) {
	resetScopes(t)
	w := &Where{}
	mergeDataScopeExpr(w, context.Background())
	assert.Nil(t, w.Expr)
}

// TestMergeDataScope_SkipsInvalidCondition 扁平版跳过无效条件（field 为空）。
func TestMergeDataScope_SkipsInvalidCondition(t *testing.T) {
	resetScopes(t)

	RegisterDataScope(func(_ context.Context, _ *authn.AuthClaims) []*storev1.FilterCondition {
		return []*storev1.FilterCondition{
			nil,                // nil 条件应跳过
			Eq("", "x"),        // 空 field 应跳过
			Eq("owner", "u-1"), // 有效
		}
	})

	w := &Where{}
	mergeDataScope(w, context.Background())
	assert.Len(t, w.Filters, 1, "只应保留有效条件")
	assert.Equal(t, "owner", w.Filters[0].GetField())
}

// TestDataScopeExpr_EndToEndThroughFacade 端到端：经门面 List 调用，
// 数据范围树应随租户条件一起下沉到 provider。
func TestDataScopeExpr_EndToEndThroughFacade(t *testing.T) {
	resetScopes(t)
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	RegisterDataScopeExpr(func(_ context.Context, c *authn.AuthClaims) *storev1.FilterExpr {
		if c == nil {
			return nil
		}
		return And([]*storev1.FilterCondition{Eq("owner", c.Subject)})
	})

	ctx := authn.ContextWithAuthClaims(context.Background(),
		&authn.AuthClaims{Subject: "u-9", TenantID: "t-9"})

	p := &recordingProvider{q: &stubQueryable{}}
	s := NewStore[facadeEntity](p)

	_, _, err := s.List(ctx, nil)
	require.NoError(t, err)

	require.NotNil(t, p.last)
	// 租户条件进 Filters。
	assert.Len(t, p.last.Filters, 1)
	assert.Equal(t, "tenant_id", p.last.Filters[0].GetField())
	// 数据范围树进 Expr。
	require.NotNil(t, p.last.Expr)
	assert.Equal(t, storev1.ExprType_AND, p.last.Expr.GetType())
}
