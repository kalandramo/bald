package store

import (
	"context"
	"errors"
	"sync"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/contextx"
)

// ErrMissingTenant 已注册租户维度，但当前 context 取不到该维度的值。
//
// 语义是**身份不完整**，不是"这条查询不需要隔离"。判据 J4 明确：
// 非平台、非系统上下文且 TenantID 为空时一律拒绝，不注入 `tenant_id = ”`。
//
// 旧实现把两种情况混为一谈——`tenantExtractors` 为空（非多租户应用，确实不该隔离）
// 与 extractor 返回空值（多租户应用 + 身份缺租户，必须拒绝）走同一个 `continue`，
// 结果是后者静默退化成"无租户约束"，全租户可读、可写。
//
// 为什么用 error 而不是"恒假条件"：恒假会让查询返回空集，调用方看到的是
// "没数据"而不是"配置错了"，后台任务会正常跑完却一条都没处理（静默失效）。
// error 是响的，空集是哑的。
//
// ⚠️ 与 bald-crud/viewer 的 ErrMissingViewer 语义重叠：`pkg/store` **不得**
// import viewer（会新增依赖边，须过 ADR-10）。消费方若同时走 store 与
// crudbridge 两条路径，需对两个 sentinel 都做 errors.Is 判定，或经 crudbridge
// 用 multi-%w 归一。只用其中一个判定会把"身份缺失"误判成"其他错误"。
var ErrMissingTenant = errors.New("store: tenant dimension registered but context carries no tenant value")

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

// DefaultTenantFunc 默认租户维度：读取认证阶段注入的 TenantID（见 pkg/authn）。
// 多租户应用需显式调用 store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
// 开启隔离；不在 init 中隐式注册，避免对存量非多租户业务静默注入隔离条件。
func DefaultTenantFunc(ctx context.Context) (string, bool) {
	if id := contextx.TenantIDFromContext(ctx); id != "" {
		return id, true
	}
	return "", false
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

// UnregisterTenant 注销一个租户维度（幂等：不存在时无操作）。
//
// 与 RegisterTenant 对偶，是全局注册点第一个补齐「逆操作」的成员（效应账本，
// 见 docs/devel/zh-CN/应用框架设计.md）。典型用法：
//
//	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
//	appkit.Effect("tenant-registration", func(ctx context.Context) error {
//	    store.UnregisterTenant("tenant_id")
//	    return nil
//	})
//
// 或 e2e 测试隔离：t.Cleanup(func() { store.UnregisterTenant("tenant_id") })。
func UnregisterTenant(key string) {
	tenantMu.Lock()
	defer tenantMu.Unlock()
	delete(tenantExtractors, key)
}

// T 标记本 Where 需注入全部已注册租户维度条件，返回副本（不修改原对象）。
//
// 业务可在构造 Where 时调用 w.T(ctx) 显式声明"本查询需租户隔离"。已注册维度
// 在 ctx 中取不到值时返回 ErrMissingTenant——**不再**静默跳过（跳过等于无租户约束，
// 是全表可见的绕过路径）。
//
// 副本保留原 Where 的全部字段（含 Expr 布尔树）——租户条件追加进 Filters，与 Expr
// 按 AND 连接，业务 OR 树不被吞掉。
func (w *Where) T(ctx context.Context) (*Where, error) {
	conds, missing := tenantConditions(ctx)
	if missing {
		return nil, ErrMissingTenant
	}
	out := &Where{
		Sorting: w.Sorting,
		Offset:  w.Offset,
		Limit:   w.Limit,
		Expr:    w.Expr,
		Filters: append(append([]*storev1.FilterCondition(nil), w.Filters...), conds...),
	}
	return out, nil
}

// tenantConditions 依据全局注册表为当前 ctx 生成租户等值条件。
//
// 第二个返回值 missing 表示「至少一个已注册维度取不到值」。必须区分两种"空"：
//
//	tenantExtractors 为空          → 非多租户应用，不隔离（missing=false，合法）
//	已注册但 extractor 返回空值     → 身份不完整，必须拒绝（missing=true）
//
// 旧实现把两者合并成一个 `if ok && v != ""`，于是第二种静默退化成不隔离。
// 判定是**逐维度**的：多维注册时任一维缺失即 missing，不做"至少一维有值就放行"
// 的聚合（那会让缺 org_id 的查询变成部分越权）。
func tenantConditions(ctx context.Context) (conds []*storev1.FilterCondition, missing bool) {
	tenantMu.RLock()
	defer tenantMu.RUnlock()
	for key, fn := range tenantExtractors {
		v, ok := fn(ctx)
		if ok && v != "" {
			conds = append(conds, Eq(key, v))
			continue
		}
		missing = true
	}
	return conds, missing
}

// mergeTenant 把租户条件注入目标 Where。
//
// 去重只认「同字段 + EQ + 同值」——即业务确实自己写了等值隔离条件。其余操作符
// （NEQ / IN / 异值 EQ）一律**追加**系统条件：AND 叠加只会更严格，绝不放宽。
//
// 旧实现按字段名去重（`if f.GetField() == tc.GetField()`），于是业务/上游只要传一条
// `NEQ tenant_id='none'` 就能把系统注入的等值条件顶掉，WHERE 变成
// `tenant_id <> 'none'`——全租户可读。这是不需要身份缺陷就能触发的绕过路径。
func mergeTenant(dst *Where, ctx context.Context) error {
	conds, missing := tenantConditions(ctx)
	if missing {
		return ErrMissingTenant
	}
	for _, tc := range conds {
		dup := false
		for _, f := range dst.Filters {
			if f.GetField() == tc.GetField() &&
				f.GetOp() == storev1.Operator_EQ &&
				f.GetValue() == tc.GetValue() {
				dup = true
				break
			}
		}
		if !dup {
			dst.Filters = append(dst.Filters, tc)
		}
	}
	return nil
}
