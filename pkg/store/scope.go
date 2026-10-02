package store

import (
	"context"
	"reflect"
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
//
// 这是**全局注册点**（对共享 slice 的写入）。若需在停机/测试隔离时撤销，
// 用 [RegisterDataScopeWithDisposer]（T1 可逆 effect 语义，与
// [RegisterTenant]/[UnregisterTenant] 的配对同构）。
func RegisterDataScope(fn DataScopeFunc) {
	scopeMu.Lock()
	defer scopeMu.Unlock()
	dataScopes = append(dataScopes, fn)
}

// RegisterDataScopeWithDisposer 注册数据范围策略并返回**注销闭包**（T1 可逆
// effect）。dispose 幂等：重复调用无副作用，且只移除本次注册项，不影响其它
// 策略。
//
// 为什么返回闭包而非提供 `UnregisterDataScope(fn)`：Go 的函数值不可比较
// （仅可与 nil 比），按「函数身份」注销不可靠——闭包每次求值都是新值。闭包
// 式 disposer 精确捕获本次注册位置，语义无歧义。
//
// 用法（与 appkit.Effect 配对，使停机逆序回放自动撤销）：
//
//	dispose := store.RegisterDataScopeWithDisposer(myScope)
//	app := appkit.New(appkit.Effect("data-scope", func(context.Context) error { dispose(); return nil }))
func RegisterDataScopeWithDisposer(fn DataScopeFunc) (dispose func()) {
	scopeMu.Lock()
	dataScopes = append(dataScopes, fn)
	scopeMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			scopeMu.Lock()
			defer scopeMu.Unlock()
			for i, f := range dataScopes {
				// 函数值不可比较，按底层代码指针定位本次注册项；含 nil 防护。
				if sameFuncPtr(f, fn) {
					dataScopes = append(dataScopes[:i], dataScopes[i+1:]...)
					return
				}
			}
		})
	}
}

// RegisterDataScopeExpr 注册一个布尔树版数据范围策略（多范围 OR 组合走这里；
// crudbridge.DataScopeFilter / RegisterDataScope 即基于此）。
//
// 可逆版本见 [RegisterDataScopeExprWithDisposer]。
func RegisterDataScopeExpr(fn DataScopeExprFunc) {
	scopeMu.Lock()
	defer scopeMu.Unlock()
	dataScopeExpr = append(dataScopeExpr, fn)
}

// RegisterDataScopeExprWithDisposer 注册布尔树版策略并返回注销闭包。
// 语义与 [RegisterDataScopeWithDisposer] 一致（幂等、只移除本次注册项）。
func RegisterDataScopeExprWithDisposer(fn DataScopeExprFunc) (dispose func()) {
	scopeMu.Lock()
	dataScopeExpr = append(dataScopeExpr, fn)
	scopeMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			scopeMu.Lock()
			defer scopeMu.Unlock()
			for i, f := range dataScopeExpr {
				if sameFuncPtr(f, fn) {
					dataScopeExpr = append(dataScopeExpr[:i], dataScopeExpr[i+1:]...)
					return
				}
			}
		})
	}
}

// ResetDataScopes 清空全部已注册的数据范围策略（扁平版 + 布尔树版）。
//
// 面向**测试隔离**与「整机复位」场景：e2e 用例间重置全局状态，避免跨用例污染。
// 生产路径应按策略粒度用 [RegisterDataScopeWithDisposer] 撤销。
func ResetDataScopes() {
	scopeMu.Lock()
	defer scopeMu.Unlock()
	dataScopes = nil
	dataScopeExpr = nil
}

// sameFuncPtr 比较两个函数值是否指向同一底层代码。
// 用于 disposer 定位「本次注册项」——多数场景下同一具名函数/同一闭包字面量的
// 代码指针稳定；配合 once 保证幂等。nil 与非 nil 不相等（防误删 nil 槽）。
func sameFuncPtr[T any](a, b T) bool {
	pa, pb := reflect.ValueOf(a), reflect.ValueOf(b)
	if pa.Kind() != reflect.Func || pb.Kind() != reflect.Func {
		return false
	}
	if pa.IsNil() || pb.IsNil() {
		return pa.IsNil() && pb.IsNil()
	}
	return pa.Pointer() == pb.Pointer()
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
