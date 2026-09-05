package crudbridge

import (
	"context"
	"strconv"

	"github.com/kalandramo/bald-crud/viewer"
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/store"
)

// DataScopeFields 配置数据范围谓词映射到实体的字段名。
type DataScopeFields struct {
	// Owner 是「归属人」字段名（SELF/USER 谓词作用于此），默认 "created_by"。
	Owner string
	// Unit 是「部门/组织」字段名（UNIT 谓词作用于此），默认 "dept_id"。
	Unit string
}

func (f DataScopeFields) owner() string {
	if f.Owner == "" {
		return "created_by"
	}
	return f.Owner
}

func (f DataScopeFields) unit() string {
	if f.Unit == "" {
		return "dept_id"
	}
	return f.Unit
}

// idList 把 DataScope.TargetIDs（uint64）转为字符串集合（FilterCondition.Values）。
func idList(ids []uint64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatUint(id, 10))
	}
	return out
}

// scopeBranch 把单个 DataScope 翻译为一个 AND 分支（作为 OR 组合的成员）。
// 返回 nil 表示该范围不产生谓词（ALL 放行 / NONE 不贡献行）。
func scopeBranch(ds viewer.DataScope, vc viewer.Context, f DataScopeFields) *storev1.FilterExpr {
	switch ds.ScopeType {
	case viewer.ScopeTypeAll:
		return nil // ALL：OR 语义下放行一切
	case viewer.ScopeTypeNone:
		return nil // NONE：不贡献任何行（若组合后无有效分支则整体恒假）
	case viewer.ScopeTypeSelf:
		return store.And([]*storev1.FilterCondition{store.Eq(f.owner(), strconv.FormatUint(vc.UserID(), 10))})
	case viewer.ScopeTypeUser:
		return store.And([]*storev1.FilterCondition{store.In(f.owner(), idList(ds.TargetIDs)...)})
	case viewer.ScopeTypeUnit:
		ids := idList(ds.TargetIDs)
		if len(ids) == 0 && vc.OrgUnitID() != 0 {
			ids = []string{strconv.FormatUint(vc.OrgUnitID(), 10)}
		}
		return store.And([]*storev1.FilterCondition{store.In(f.unit(), ids...)})
	default:
		return nil // 未知范围类型：不猜语义（宁缺勿假）
	}
}

// DataScopeFilter 按 viewer 携带的数据范围列表生成长级过滤布尔树。
//
// 五级语义（与 bald-crud viewer 对齐），多范围之间为 **OR** 组合：
//   - ALL：放行（返回 nil，不附加任何条件）；
//   - SELF：owner 字段 = 当前用户；
//   - USER：owner 字段 IN TargetIDs；
//   - UNIT：unit 字段 IN TargetIDs（TargetIDs 为空时回退 OrgUnitID）；
//   - NONE：不贡献任何行。
//
// fail-closed 约定：viewer 为 nil 或未声明任何范围时返回**空 OR 节点（恒假）**
// ——调用方既然对实体启用了数据范围管控，匿名/未配置身份就不应看到任何行。
// 组合后若无有效分支（例如只有 NONE）同样恒假。
func DataScopeFilter(vc viewer.Context, f DataScopeFields) *storev1.FilterExpr {
	if vc == nil {
		return store.Or(nil) // 空 OR = 恒假（fail-closed）
	}
	scopes := vc.DataScope()
	if len(scopes) == 0 {
		return store.Or(nil)
	}
	var branches []*storev1.FilterExpr
	sawAll := false
	for _, ds := range scopes {
		if ds.ScopeType == viewer.ScopeTypeAll {
			sawAll = true
			break
		}
	}
	if sawAll {
		return nil // ALL 覆盖 OR 的一切分支：放行
	}
	for _, ds := range scopes {
		if b := scopeBranch(ds, vc, f); b != nil {
			branches = append(branches, b)
		}
	}
	if len(branches) == 0 {
		return store.Or(nil) // 只有 NONE / 未知类型 → 恒假
	}
	if len(branches) == 1 {
		return branches[0] // 单范围无需 OR 包装，减少嵌套
	}
	return store.Or(nil, branches...)
}

// RegisterDataScope 是「一行接入」便捷函数：注册一个布尔树版数据范围策略，
// 内部从 context 读取已注入的 viewer.Context（认证中间件内置注入）并翻译为
// 五级范围谓词。业务只需：
//
//	crudbridge.RegisterDataScope(crudbridge.DataScopeFields{})   // 默认 created_by/dept_id
//	crudbridge.RegisterDataScope(crudbridge.DataScopeFields{Owner: "owner_id"})
func RegisterDataScope(f DataScopeFields) {
	store.RegisterDataScopeExpr(func(ctx context.Context, _ *authn.AuthClaims) *storev1.FilterExpr {
		vc, _ := viewer.FromContext(ctx)
		return DataScopeFilter(vc, f)
	})
}
