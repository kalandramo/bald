package store

import (
	"context"
	"sync"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/authn"
)

// DataScopeFunc 依据当前请求身份（AuthClaims）计算额外的数据范围过滤条件。
//
// 这是 bald-crud 的 Viewer（五级数据范围：SELF/UNIT/USER/ALL/NONE）在 bald 的
// 依赖倒置实现：核心只定义函数签名，具体范围策略（按角色/部门/owner 字段）由业务
// 通过 RegisterDataScope 注入，核心与具体权限模型解耦。
//
// 返回的条件会追加进 Where（与租户隔离同机制），业务 handler 无需手写，避免越权。
type DataScopeFunc func(ctx context.Context, claims *authn.AuthClaims) []*storev1.FilterCondition

// DataScopeExprFunc 是 DataScopeFunc 的布尔树版：返回完整 FilterExpr（支持
// 多范围 OR 组合，如「本人 OR 本部门」）。与扁平版并存，语义均按 AND 并入查询。
type DataScopeExprFunc func(ctx context.Context, claims *authn.AuthClaims) *storev1.FilterExpr

var (
	scopeMu       sync.RWMutex
	dataScopes    []DataScopeFunc
	dataScopeExpr []DataScopeExprFunc
)

// RegisterDataScope 注册一个数据范围策略（可多次注册，多个策略的条件会合并）。
func RegisterDataScope(fn DataScopeFunc) {
	scopeMu.Lock()
	defer scopeMu.Unlock()
	dataScopes = append(dataScopes, fn)
}

// RegisterDataScopeExpr 注册一个布尔树版数据范围策略（多范围 OR 组合走这里；
// crudbridge.DataScopeFilter / RegisterDataScope 即基于此）。
func RegisterDataScopeExpr(fn DataScopeExprFunc) {
	scopeMu.Lock()
	defer scopeMu.Unlock()
	dataScopeExpr = append(dataScopeExpr, fn)
}

// mergeDataScope 把已注册的数据范围条件注入 Where（在租户隔离之后，进一步收窄可见行）。
func mergeDataScope(dst *Where, ctx context.Context) {
	claims := authn.AuthClaimsFromContext(ctx)
	scopeMu.RLock()
	fns := append([]DataScopeFunc(nil), dataScopes...)
	scopeMu.RUnlock()
	for _, fn := range fns {
		for _, c := range fn(ctx, claims) {
			if c == nil || c.GetField() == "" {
				continue // 跳过无效条件，避免翻译期拼出非法过滤
			}
			dst.Filters = append(dst.Filters, c)
		}
	}
}

// mergeDataScopeExpr 把布尔树版数据范围策略并入 Where.Expr。
// 语义：Expr = AND(scopeExprs..., 业务 Expr)——数据范围与业务过滤条件互不干扰。
func mergeDataScopeExpr(dst *Where, ctx context.Context) {
	claims := authn.AuthClaimsFromContext(ctx)
	scopeMu.RLock()
	fns := append([]DataScopeExprFunc(nil), dataScopeExpr...)
	scopeMu.RUnlock()
	var exprs []*storev1.FilterExpr
	for _, fn := range fns {
		if fe := fn(ctx, claims); fe != nil {
			exprs = append(exprs, fe)
		}
	}
	if len(exprs) == 0 {
		return
	}
	if dst.Expr != nil {
		exprs = append(exprs, dst.Expr)
	}
	if len(exprs) == 1 {
		dst.Expr = exprs[0]
		return
	}
	dst.Expr = And(nil, exprs...) // 多棵树按 AND 组合
}
