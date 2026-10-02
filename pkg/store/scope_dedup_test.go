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
// P2-4 审查结论：数据范围**有意不做同名去重**（与 mergeTenant 不同）
//
// 原计划设想「mergeDataScope 缺去重，与 mergeTenant 不对称 → 补去重」。
// 经审查，该设想在安全维度上是**反向的**——去重会 fail-open：
//
//	业务 Where 写 owner_id=u2（例如用户显式传入的过滤参数）
//	scope 策略出 owner_id=u1（当前登录者的可见范围）
//	  · 去重后：仅 owner_id=u2  → 用户看到 u2 的数据（**越权**）
//	  · 不去重：u2 AND u1 = 空  → 查不到（安全拒绝）
//
// 数据范围的角色是**行级收窄**（"进一步收窄可见行"，见 mergeDataScope 注释），
// 其设计取向明确是 fail-closed——`crudbridge/data_scope.go:76-104` 载明
// 「viewer 为 nil 或未声明任何范围时返回**空 OR 节点（恒假）**」。
// 因此「重复条件叠加成空」正是期望行为，不是缺陷。
//
// mergeTenant 的去重理由不同：租户列的语义是「这条记录属于哪个租户」，业务
// 写该列即视为自行承担隔离语义（见 mergeTenant 注释）。二者不可类比。
//
// 本文件用测试钉住该语义，防止后来者「顺手统一」两个 merge 函数而引入安全回归。
// ---------------------------------------------------------------------------

// TestMergeDataScope_BusinessSameFieldDoesNotBypassScope 业务与 scope 命中同一
// 字段时，scope 条件**必须保留**（叠加求交，绝不能被业务条件顶掉）。
func TestMergeDataScope_BusinessSameFieldDoesNotBypassScope(t *testing.T) {
	resetScopes(t)

	RegisterDataScope(func(_ context.Context, _ *authn.AuthClaims) []*storev1.FilterCondition {
		return []*storev1.FilterCondition{Eq("owner_id", "u-scope")}
	})

	// 业务显式传入一个不在可见范围内的 owner 过滤（模拟越权尝试）。
	w := &Where{Filters: []*storev1.FilterCondition{Eq("owner_id", "u-business")}}
	mergeDataScope(w, context.Background())

	require.Len(t, w.Filters, 2, "scope 条件不得因业务同字段而被丢弃（否则 fail-open）")
	fields := []string{w.Filters[0].GetField(), w.Filters[1].GetField()}
	assert.Equal(t, []string{"owner_id", "owner_id"}, fields,
		"两条 owner_id 条件并存 → 查询求交 → 越权项查不到（安全拒绝）")
}

// TestMergeDataScope_MultipleStrategiesNotDeduped 多策略命中同字段同样全部保留
// （不去重；叠加求交是期望语义）。
func TestMergeDataScope_MultipleStrategiesNotDeduped(t *testing.T) {
	resetScopes(t)

	RegisterDataScope(func(_ context.Context, _ *authn.AuthClaims) []*storev1.FilterCondition {
		return []*storev1.FilterCondition{Eq("dept_id", "d-1")}
	})
	RegisterDataScope(func(_ context.Context, _ *authn.AuthClaims) []*storev1.FilterCondition {
		return []*storev1.FilterCondition{Eq("dept_id", "d-2")}
	})

	w := &Where{}
	mergeDataScope(w, context.Background())
	assert.Len(t, w.Filters, 2, "多策略条件全部注入，不去重")
}

// TestMergeDataScope_DistinctFieldsAllKept 不同字段全部保留。
func TestMergeDataScope_DistinctFieldsAllKept(t *testing.T) {
	resetScopes(t)

	RegisterDataScope(func(_ context.Context, _ *authn.AuthClaims) []*storev1.FilterCondition {
		return []*storev1.FilterCondition{Eq("owner_id", "u-1"), Eq("dept_id", "d-1")}
	})

	w := &Where{}
	mergeDataScope(w, context.Background())
	require.Len(t, w.Filters, 2, "不同字段应全部保留")
}

// TestMergeDataScopeExpr_ScopeTreeNotBypassedByBusinessTree 布尔树版：业务树与
// scope 树并存时按 AND 组合——scope 分支绝不能被丢弃。
func TestMergeDataScopeExpr_ScopeTreeNotBypassedByBusinessTree(t *testing.T) {
	resetScopes(t)

	scopeTree := And([]*storev1.FilterCondition{Eq("owner_id", "u-scope")})
	RegisterDataScopeExpr(func(_ context.Context, _ *authn.AuthClaims) *storev1.FilterExpr {
		return scopeTree
	})

	biz := And([]*storev1.FilterCondition{Eq("owner_id", "u-business")})
	w := &Where{Expr: biz}
	mergeDataScopeExpr(w, context.Background())

	require.NotNil(t, w.Expr)
	assert.Equal(t, storev1.ExprType_AND, w.Expr.GetType(),
		"scope 树与业务树必须 AND 组合（丢弃任一方都会 fail-open 或功能失效）")
	assert.Len(t, w.Expr.GetGroups(), 2, "scope 分支必须保留")
}

// TestMergeDataScopeExpr_DistinctAnded 字段不同则正常 AND。
func TestMergeDataScopeExpr_DistinctAnded(t *testing.T) {
	resetScopes(t)

	RegisterDataScopeExpr(func(_ context.Context, _ *authn.AuthClaims) *storev1.FilterExpr {
		return And([]*storev1.FilterCondition{Eq("dept_id", "d-1")})
	})

	biz := And([]*storev1.FilterCondition{Eq("owner_id", "u-business")})
	w := &Where{Expr: biz}
	mergeDataScopeExpr(w, context.Background())

	require.NotNil(t, w.Expr)
	assert.Equal(t, storev1.ExprType_AND, w.Expr.GetType())
	assert.Len(t, w.Expr.GetGroups(), 2)
}
