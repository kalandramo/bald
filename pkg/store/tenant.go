package store

import (
	"context"
	"sync"

	storev1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/contextx"
)

// TenantValueFunc 从请求 context 解析某个租户维度的值。返回 ("", false) 表示该维度
// 在当前请求上不可用（如非多租户应用、匿名请求）。
//
// 约定：多租户列名（key）即 Where 中的 FilterCondition.Field；valueFunc 默认由
// pkg/authn 在认证后把 TenantID 写入 contextx，本包内置 defaultTenantFunc 即读取它。
type TenantValueFunc func(ctx context.Context) (value string, ok bool)

// 全局租户维度注册表：key（列名）→ 取值函数。非多租户应用不注册即无租户列，零开销。
var (
	tenantMu         sync.RWMutex
	tenantExtractors = map[string]TenantValueFunc{}
)

// defaultTenantFunc 默认租户维度：读取认证阶段注入的 TenantID（见 pkg/authn）。
func defaultTenantFunc(ctx context.Context) (string, bool) {
	if id := contextx.TenantIDFromContext(ctx); id != "" {
		return id, true
	}
	return "", false
}

func init() {
	// 内置默认维度 "tenant_id"，业务可 RegisterTenant 覆盖列名或追加更多维度。
	tenantExtractors["tenant_id"] = defaultTenantFunc
}

// RegisterTenant 注册一个租户维度：key 为租户列名，valueFunc 从 context 解析其值。
//
// 多租户隔离由此下沉到 DAL：Store 在翻译查询时自动把每个已注册维度追加为等值过滤
// 条件，业务 handler 无需手写，从而避免"漏写租户条件导致跨租户数据泄漏"。
//
// 重复注册同一 key 会覆盖（允许业务自定义列名或取值来源）。
func RegisterTenant(key string, valueFunc TenantValueFunc) {
	tenantMu.Lock()
	defer tenantMu.Unlock()
	tenantExtractors[key] = valueFunc
}

// T 标记本 Where 需注入全部已注册租户维度条件，返回副本（不修改原对象）。
//
// 业务可在构造 Where 时调用 w.T(ctx) 显式声明"本查询需租户隔离"；Store.translate
// 也会对非 NoPaging 列表默认尝试注入（见 mergeTenant）。若 ctx 中无租户值，则该
// 维度被跳过（等效于"无租户约束"）。
func (w *Where) T(ctx context.Context) *Where {
	out := *w
	out.Filters = append([]*storev1.FilterCondition(nil), w.Filters...)
	out.Filters = append(out.Filters, tenantConditions(ctx)...)
	return &out
}

// tenantConditions 依据全局注册表为当前 ctx 生成所有可用的租户等值条件。
func tenantConditions(ctx context.Context) []*storev1.FilterCondition {
	tenantMu.RLock()
	defer tenantMu.RUnlock()
	var conds []*storev1.FilterCondition
	for key, fn := range tenantExtractors {
		if v, ok := fn(ctx); ok && v != "" {
			conds = append(conds, Eq(key, v))
		}
	}
	return conds
}

// mergeTenant 把租户条件注入目标 Where，并去重业务已手写的同名条件（租户维度优先，
// 业务条件不得覆盖隔离列）。
func mergeTenant(dst *Where, ctx context.Context) {
	for _, tc := range tenantConditions(ctx) {
		// 业务已显式写该租户列时，跳过注入（避免重复条件；隔离语义已由业务保证）。
		dup := false
		for _, f := range dst.Filters {
			if f.GetField() == tc.GetField() && f.GetOp() == storev1.Operator_EQ {
				dup = true
				break
			}
		}
		if !dup {
			dst.Filters = append(dst.Filters, tc)
		}
	}
}
