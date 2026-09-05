package store

import (
	"context"
	"sync"

	"github.com/kalandramo/bald/pkg/authn"
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
)

// DataScopeFunc 依据当前请求身份（AuthClaims）计算额外的数据范围过滤条件。
//
// 这是 go-crud 的 Viewer（五级数据范围：全部/租户/部门/本人/自定义）在 bald 的
// 依赖倒置实现：核心只定义函数签名，具体范围策略（按角色/部门/owner 字段）由业务
// 通过 RegisterDataScope 注入，核心与具体权限模型解耦。
//
// 返回的条件会追加进 Where（与租户隔离同机制），业务 handler 无需手写，避免越权。
type DataScopeFunc func(ctx context.Context, claims *authn.AuthClaims) []*storev1.FilterCondition

var (
	scopeMu    sync.RWMutex
	dataScopes []DataScopeFunc
)

// RegisterDataScope 注册一个数据范围策略（可多次注册，多个策略的条件会合并）。
func RegisterDataScope(fn DataScopeFunc) {
	scopeMu.Lock()
	defer scopeMu.Unlock()
	dataScopes = append(dataScopes, fn)
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
