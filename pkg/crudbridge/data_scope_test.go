package crudbridge_test

import (
	"context"
	"testing"

	"github.com/kalandramo/bald-crud/viewer"
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/crudbridge"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/kalandramo/bald/pkg/store/inmemory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type doc struct {
	ID        string
	CreatedBy string
	DeptID    string
	Title     string
}

func dsView(scopes ...viewer.DataScope) viewer.Context {
	return &crudbridge.SimpleViewer{UserIDValue: 7, TenantIDValue: 1001, ScopesValue: scopes}
}

// noPagingReq 构造不分页请求（ListWithPaging 要求非 nil）。
func noPagingReq() *storev1.PagingRequest {
	v := true
	return &storev1.PagingRequest{NoPaging: &v}
}

func TestDataScopeFilter_SingleScopes(t *testing.T) {
	f := crudbridge.DataScopeFields{}

	t.Run("ALL → 放行（nil）", func(t *testing.T) {
		assert.Nil(t, crudbridge.DataScopeFilter(dsView(viewer.DataScope{ScopeType: viewer.ScopeTypeAll}), f))
	})

	t.Run("NONE → 空 OR 恒假", func(t *testing.T) {
		fe := crudbridge.DataScopeFilter(dsView(viewer.DataScope{ScopeType: viewer.ScopeTypeNone}), f)
		require.NotNil(t, fe)
		assert.Equal(t, storev1.ExprType_OR, fe.GetType())
		assert.Empty(t, fe.GetConditions())
		assert.Empty(t, fe.GetGroups())
	})

	t.Run("SELF → owner = 当前用户", func(t *testing.T) {
		fe := crudbridge.DataScopeFilter(dsView(viewer.DataScope{ScopeType: viewer.ScopeTypeSelf}), f)
		require.Len(t, fe.GetConditions(), 1)
		c := fe.GetConditions()[0]
		assert.Equal(t, "created_by", c.GetField())
		assert.Equal(t, storev1.Operator_EQ, c.GetOp())
		assert.Equal(t, "7", c.GetValue())
	})

	t.Run("USER → owner IN TargetIDs", func(t *testing.T) {
		fe := crudbridge.DataScopeFilter(dsView(viewer.DataScope{
			ScopeType: viewer.ScopeTypeUser, TargetIDs: []uint64{7, 9},
		}), f)
		require.Len(t, fe.GetConditions(), 1)
		c := fe.GetConditions()[0]
		assert.Equal(t, storev1.Operator_IN, c.GetOp())
		assert.Equal(t, []string{"7", "9"}, c.GetValues())
	})

	t.Run("UNIT → unit IN TargetIDs（空时回退 OrgUnitID）", func(t *testing.T) {
		fe := crudbridge.DataScopeFilter(dsView(viewer.DataScope{
			ScopeType: viewer.ScopeTypeUnit, TargetIDs: []uint64{31},
		}), f)
		require.Len(t, fe.GetConditions(), 1)
		assert.Equal(t, "dept_id", fe.GetConditions()[0].GetField())
		assert.Equal(t, []string{"31"}, fe.GetConditions()[0].GetValues())

		vc := &crudbridge.SimpleViewer{UserIDValue: 7, OrgUnitIDValue: 42,
			ScopesValue: []viewer.DataScope{{ScopeType: viewer.ScopeTypeUnit}}}
		fe = crudbridge.DataScopeFilter(vc, f)
		require.Len(t, fe.GetConditions(), 1)
		assert.Equal(t, []string{"42"}, fe.GetConditions()[0].GetValues())
	})
}

func TestDataScopeFilter_Combination(t *testing.T) {
	f := crudbridge.DataScopeFields{}

	t.Run("SELF + UNIT → OR 组合（本人 OR 本部门）", func(t *testing.T) {
		fe := crudbridge.DataScopeFilter(dsView(
			viewer.DataScope{ScopeType: viewer.ScopeTypeSelf},
			viewer.DataScope{ScopeType: viewer.ScopeTypeUnit, TargetIDs: []uint64{31}},
		), f)
		require.NotNil(t, fe)
		assert.Equal(t, storev1.ExprType_OR, fe.GetType())
		require.Len(t, fe.GetGroups(), 2) // 两个 AND 分支
	})

	t.Run("ALL 与任意范围并存 → 放行", func(t *testing.T) {
		assert.Nil(t, crudbridge.DataScopeFilter(dsView(
			viewer.DataScope{ScopeType: viewer.ScopeTypeSelf},
			viewer.DataScope{ScopeType: viewer.ScopeTypeAll},
		), f))
	})

	t.Run("viewer 未声明范围 → 恒假（fail-closed）", func(t *testing.T) {
		fe := crudbridge.DataScopeFilter(&crudbridge.SimpleViewer{}, f)
		require.NotNil(t, fe)
		assert.Equal(t, storev1.ExprType_OR, fe.GetType())
		assert.Empty(t, fe.GetGroups())
	})

	t.Run("自定义字段映射", func(t *testing.T) {
		fe := crudbridge.DataScopeFilter(dsView(viewer.DataScope{ScopeType: viewer.ScopeTypeSelf}),
			crudbridge.DataScopeFields{Owner: "owner_id"})
		assert.Equal(t, "owner_id", fe.GetConditions()[0].GetField())
	})
}

// TestDataScope_InmemoryIntegration 全链路集成：一行注册数据范围 → inmemory 查询
// 自动收窄（OR 树生效、与业务条件 AND 连接）。
func TestDataScope_InmemoryIntegration(t *testing.T) {
	crudbridge.RegisterDataScope(crudbridge.DataScopeFields{})

	p := inmemory.NewProvider[doc](func(d *doc) string { return d.ID })
	repo := store.NewStore[doc](p)
	ctx := context.Background()
	for _, d := range []doc{
		{ID: "1", CreatedBy: "7", DeptID: "31", Title: "mine"},
		{ID: "2", CreatedBy: "9", DeptID: "31", Title: "peer same dept"},
		{ID: "3", CreatedBy: "9", DeptID: "32", Title: "other dept"},
	} {
		require.NoError(t, repo.Create(ctx, &d))
	}

	// 本人 OR 本部门（31）：命中 doc1（本人）、doc2（同部门），doc3 不命中。
	// 注意走 ListWithPaging：数据范围/租户注入在 translate 链路生效（List 是底层直通）。
	vc := dsView(
		viewer.DataScope{ScopeType: viewer.ScopeTypeSelf},
		viewer.DataScope{ScopeType: viewer.ScopeTypeUnit, TargetIDs: []uint64{31}},
	)
	dctx := crudbridge.InjectViewer(ctx, vc)
	res, err := repo.ListWithPaging(dctx, noPagingReq())
	require.NoError(t, err)
	require.Equal(t, uint64(2), res.Meta.GetTotal().GetValue())
	require.Len(t, res.Items, 2)

	// 无范围声明（未注入 viewer）→ 恒假，什么都看不到。
	res, err = repo.ListWithPaging(ctx, noPagingReq())
	require.NoError(t, err)
	assert.Equal(t, uint64(0), res.Meta.GetTotal().GetValue())

	// ALL → 放行全部。
	dctx = crudbridge.InjectViewer(ctx, dsView(viewer.DataScope{ScopeType: viewer.ScopeTypeAll}))
	res, err = repo.ListWithPaging(dctx, noPagingReq())
	require.NoError(t, err)
	assert.Equal(t, uint64(3), res.Meta.GetTotal().GetValue())
}
