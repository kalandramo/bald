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
// T1 可逆性：数据范围策略的注销原语
// （RegisterDataScopeWithDisposer / RegisterDataScopeExprWithDisposer / ResetDataScopes）
//
// 背景：`store.RegisterDataScope` / `RegisterDataScopeExpr` 是全局注册点，
// 但**对偶的逆操作原语缺失**——对比 `store.RegisterTenant` / `UnregisterTenant`
// 已配对。后果：e2e 用 `t.Cleanup(app.UndoEffects)` 隔离全局状态时，数据范围
// 策略泄漏到下一个用例（跨用例污染）；进程内多 AppKit 实例场景下注册叠加。
//
// 为什么用 WithDisposer 而非 `UnregisterDataScope(fn)`：Go 的函数值不可比较，
// 按「函数身份」注销不可靠（闭包每次求值都是新值）。闭包式 disposer 精确捕获
// 本次注册项，语义无歧义。
// ---------------------------------------------------------------------------

// TestRegisterDataScopeWithDisposer_RemovesStrategy 注销后扁平版策略不再生效。
func TestRegisterDataScopeWithDisposer_RemovesStrategy(t *testing.T) {
	resetScopes(t)

	dispose := RegisterDataScopeWithDisposer(func(_ context.Context, _ *authn.AuthClaims) []*storev1.FilterCondition {
		return []*storev1.FilterCondition{Eq("owner", "u-1")}
	})

	before := &Where{}
	mergeDataScope(before, context.Background())
	require.Len(t, before.Filters, 1, "注册后策略应生效")

	dispose()

	after := &Where{}
	mergeDataScope(after, context.Background())
	assert.Empty(t, after.Filters, "disposer 调用后策略应消失")
}

// TestRegisterDataScopeExprWithDisposer_RemovesStrategy 注销后布尔树版策略不再生效。
func TestRegisterDataScopeExprWithDisposer_RemovesStrategy(t *testing.T) {
	resetScopes(t)

	dispose := RegisterDataScopeExprWithDisposer(func(_ context.Context, _ *authn.AuthClaims) *storev1.FilterExpr {
		return And([]*storev1.FilterCondition{Eq("dept_id", "d-1")})
	})

	before := &Where{}
	mergeDataScopeExpr(before, context.Background())
	require.NotNil(t, before.Expr, "注册后布尔树策略应生效")

	dispose()

	after := &Where{}
	mergeDataScopeExpr(after, context.Background())
	assert.Nil(t, after.Expr, "disposer 调用后布尔树策略应消失")
}

// TestRegisterDataScopeWithDisposer_Idempotent 重复调用 disposer 幂等（无副作用），
// 且只移除本次注册项——不影响其它已注册策略。
func TestRegisterDataScopeWithDisposer_Idempotent(t *testing.T) {
	resetScopes(t)

	disposeA := RegisterDataScopeWithDisposer(func(_ context.Context, _ *authn.AuthClaims) []*storev1.FilterCondition {
		return []*storev1.FilterCondition{Eq("a", "1")}
	})
	RegisterDataScope(func(_ context.Context, _ *authn.AuthClaims) []*storev1.FilterCondition {
		return []*storev1.FilterCondition{Eq("b", "2")}
	})

	disposeA()
	disposeA() // 幂等：重复调用不 panic、不误删他人

	w := &Where{}
	mergeDataScope(w, context.Background())
	require.Len(t, w.Filters, 1, "只应剩下未注销的那个策略")
	assert.Equal(t, "b", w.Filters[0].GetField())
}

// TestResetDataScopes_ClearsAll 复位清空全部已注册策略（flat + expr）。
func TestResetDataScopes_ClearsAll(t *testing.T) {
	resetScopes(t)

	RegisterDataScope(func(_ context.Context, _ *authn.AuthClaims) []*storev1.FilterCondition {
		return []*storev1.FilterCondition{Eq("a", "1")}
	})
	RegisterDataScopeExpr(func(_ context.Context, _ *authn.AuthClaims) *storev1.FilterExpr {
		return And([]*storev1.FilterCondition{Eq("b", "2")})
	})

	ResetDataScopes()

	w := &Where{}
	mergeDataScope(w, context.Background())
	mergeDataScopeExpr(w, context.Background())
	assert.Empty(t, w.Filters, "ResetDataScopes 应清空扁平版策略")
	assert.Nil(t, w.Expr, "ResetDataScopes 应清空布尔树版策略")
}
