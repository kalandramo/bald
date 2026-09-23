package baldmongo

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Doc 是带 JSON/数组字段的测试实体（用于 JSON_CONTAINS/ARRAY_CONTAINS/EXISTS）。
type Doc struct {
	ID   string   `bson:"id"`
	Tags []string `bson:"tags"`
	Meta Meta     `bson:"meta"`
}

// Meta 是嵌套子文档。
type Meta struct {
	Role string `bson:"role"`
}

func newDocStore(t *testing.T) *store.Store[Doc] {
	t.Helper()
	db := newTestDB(t)
	p := NewMongoProvider[Doc](db, "docs", func(d *Doc) string { return d.ID })
	require.NoError(t, p.Migrate(context.Background()))
	return store.NewStore[Doc](p)
}

// TestMongo_ArrayContains 锁定 ARRAY_CONTAINS 原生实现（MongoDB 数组字段含元素）。
// 原缺陷：走 default 恒真（全放行），条件被忽略。
func TestMongo_ArrayContains(t *testing.T) {
	repo := newDocStore(t)
	ctx := context.Background()
	for _, d := range []Doc{
		{ID: "1", Tags: []string{"a", "b"}},
		{ID: "2", Tags: []string{"c"}},
		{ID: "3", Tags: []string{"b", "d"}},
	} {
		d := d
		require.NoError(t, repo.Create(ctx, &d))
	}

	// tags 含 "b" → doc1, doc3（不含 doc2）
	res, err := repo.ListWithPaging(ctx, &storev1.PagingRequest{
		FilteringType: &storev1.PagingRequest_FilterExpr{FilterExpr: &storev1.FilterExpr{
			Conditions: []*storev1.FilterCondition{{
				Field: "tags", Op: storev1.Operator_ARRAY_CONTAINS, ValueOneof: &storev1.FilterCondition_Value{Value: "b"},
			}},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(2), res.Meta.GetTotal().GetValue())
	assert.Len(t, res.Items, 2)
}

// TestMongo_JSONContains 锁定 JSON_CONTAINS 原生实现（MongoDB 点号路径匹配）。
func TestMongo_JSONContains(t *testing.T) {
	repo := newDocStore(t)
	ctx := context.Background()
	for _, d := range []Doc{
		{ID: "1", Meta: Meta{Role: "admin"}},
		{ID: "2", Meta: Meta{Role: "user"}},
		{ID: "3", Meta: Meta{Role: "admin"}},
	} {
		d := d
		require.NoError(t, repo.Create(ctx, &d))
	}

	// meta.role = admin → doc1, doc3
	res, err := repo.ListWithPaging(ctx, &storev1.PagingRequest{
		FilteringType: &storev1.PagingRequest_FilterExpr{FilterExpr: &storev1.FilterExpr{
			Conditions: []*storev1.FilterCondition{{
				Field: "meta.role", Op: storev1.Operator_JSON_CONTAINS, ValueOneof: &storev1.FilterCondition_Value{Value: "admin"},
			}},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(2), res.Meta.GetTotal().GetValue())
	assert.Len(t, res.Items, 2)
}

// TestMongo_Exists 锁定 EXISTS 原生实现（MongoDB $exists）。
func TestMongo_Exists(t *testing.T) {
	repo := newDocStore(t)
	ctx := context.Background()
	for _, d := range []Doc{
		{ID: "1", Meta: Meta{Role: "admin"}},
		{ID: "2"}, // Role 空
	} {
		d := d
		require.NoError(t, repo.Create(ctx, &d))
	}

	// meta.role 存在 → doc1（doc2 的 role 是零值 ""，仍写入字段）
	// 注：MongoDB 中零值字段会被写入（除非 omitempty），故 $exists:true 命中两条；
	// 本测试锁定"字段存在性"语义，不断言业务零值含义。
	res, err := repo.ListWithPaging(ctx, &storev1.PagingRequest{
		FilteringType: &storev1.PagingRequest_FilterExpr{FilterExpr: &storev1.FilterExpr{
			Conditions: []*storev1.FilterCondition{{
				Field: "meta.role", Op: storev1.Operator_EXISTS, ValueOneof: &storev1.FilterCondition_Value{Value: "true"},
			}},
		}},
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, res.Meta.GetTotal().GetValue(), uint64(1))

	// 不存在的字段 → 0
	res, err = repo.ListWithPaging(ctx, &storev1.PagingRequest{
		FilteringType: &storev1.PagingRequest_FilterExpr{FilterExpr: &storev1.FilterExpr{
			Conditions: []*storev1.FilterCondition{{
				Field: "meta.nope", Op: storev1.Operator_EXISTS, ValueOneof: &storev1.FilterCondition_Value{Value: "true"},
			}},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(0), res.Meta.GetTotal().GetValue())
}
