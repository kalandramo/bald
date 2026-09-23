package baldmongo

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// User 是测试实体（BSON 模型）。Transient 是 `bson:"-"` 内存态字段——
// 回归锁：Update 的反射文档不得把它当集合字段。
type User struct {
	ID        string `bson:"id"`
	Name      string `bson:"name"`
	Age       int    `bson:"age"`
	Transient string `bson:"-"`
}

func newTestStore(t *testing.T) *store.Store[User] {
	t.Helper()
	db := newTestDB(t)
	p := NewMongoProvider[User](db, "users", func(u *User) string { return u.ID })
	require.NoError(t, p.Migrate(context.Background()))
	return store.NewStore[User](p)
}

func TestMongoCRUD(t *testing.T) {
	repo := newTestStore(t)
	ctx := context.Background()

	// Create
	require.NoError(t, repo.Create(ctx, &User{ID: "1", Name: "alice", Age: 30}))
	require.NoError(t, repo.Create(ctx, &User{ID: "2", Name: "bob", Age: 25}))
	// 冲突（id 唯一索引）
	assert.ErrorIs(t, repo.Create(ctx, &User{ID: "1", Name: "dup", Age: 1}), store.ErrConflict)

	// Get
	got, err := repo.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	require.NoError(t, err)
	assert.Equal(t, "alice", got.Name)

	// Update（含 bson:"-" 内存态字段：不得被当作集合字段）
	rows, err := repo.Update(ctx, &User{ID: "1", Name: "alice2", Age: 31, Transient: "ignored"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)
	got, err = repo.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	require.NoError(t, err)
	assert.Equal(t, "alice2", got.Name)
	assert.Equal(t, 31, got.Age)
	assert.Empty(t, got.Transient) // "-" 字段不落库

	// Count
	n, err := repo.Count(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	// Delete
	_, err = repo.Delete(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "2")}})
	require.NoError(t, err)
	_, err = repo.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "2")}})
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestMongoFilterSortPaging(t *testing.T) {
	repo := newTestStore(t)
	ctx := context.Background()

	for _, u := range []User{
		{ID: "1", Name: "alice", Age: 30},
		{ID: "2", Name: "bob", Age: 25},
		{ID: "3", Name: "carol", Age: 35},
		{ID: "4", Name: "dave", Age: 25},
	} {
		u := u
		require.NoError(t, repo.Create(ctx, &u))
	}

	// 过滤：age >= 25 且 name 含 "a"
	res, err := repo.ListWithPaging(ctx, &storev1.PagingRequest{
		FilteringType: &storev1.PagingRequest_FilterExpr{FilterExpr: &storev1.FilterExpr{
			Conditions: []*storev1.FilterCondition{
				store.Gte("age", "25"),
				store.Contains("name", "a"),
			},
		}},
		Sorting: []*storev1.Sorting{store.SortDesc("age")},
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 3) // alice(30), carol(35), dave(25)；bob 不含 a
	assert.Equal(t, "carol", res.Items[0].Name)
	assert.Equal(t, uint64(3), res.Meta.GetTotal().GetValue())

	// 分页：第 1 页每页 2
	res, err = repo.ListWithPaging(ctx, &storev1.PagingRequest{
		Page:     protoUint32(1),
		PageSize: protoUint32(2),
		Sorting:  []*storev1.Sorting{store.Sort("age")},
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 2)
	assert.Equal(t, uint32(2), res.Meta.GetCurrentSize())
	assert.Equal(t, uint64(4), res.Meta.GetTotal().GetValue())
}

// TestMongo_OrExpr 锁定布尔树翻译：OR 分支正确组合，空 OR 恒假。
func TestMongo_OrExpr(t *testing.T) {
	repo := newTestStore(t)
	ctx := context.Background()
	for _, u := range []User{
		{ID: "1", Name: "alice", Age: 30},
		{ID: "2", Name: "bob", Age: 16},
		{ID: "3", Name: "carol", Age: 70},
	} {
		u := u
		require.NoError(t, repo.Create(ctx, &u))
	}

	// OR(name=alice, name=carol) → 2
	res, err := repo.ListWithPaging(ctx, &storev1.PagingRequest{
		FilteringType: &storev1.PagingRequest_FilterExpr{FilterExpr: store.Or([]*storev1.FilterCondition{
			store.Eq("name", "alice"),
			store.Eq("name", "carol"),
		})},
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 2)

	// 嵌套：OR(name=bob, age>60) → bob, carol
	res, err = repo.ListWithPaging(ctx, &storev1.PagingRequest{
		FilteringType: &storev1.PagingRequest_FilterExpr{FilterExpr: store.Or(nil,
			store.Or([]*storev1.FilterCondition{store.Eq("name", "bob")}),
			store.Or([]*storev1.FilterCondition{store.Gt("age", "60")}),
		)},
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 2)

	// 空 OR 恒假：查不到任何行
	res, err = repo.ListWithPaging(ctx, &storev1.PagingRequest{
		FilteringType: &storev1.PagingRequest_FilterExpr{FilterExpr: store.Or(nil)},
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 0)
	assert.Equal(t, uint64(0), res.Meta.GetTotal().GetValue())
}

// TestMongo_UpdateIdempotent 锁定 Update 幂等契约（与 Delete 对称）。
// 注意 Mongo 返回 MatchedCount（匹配行数）——「更新到相同值」仍返回 1。
func TestMongo_UpdateIdempotent(t *testing.T) {
	repo := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, &User{ID: "1", Name: "alice", Age: 30}))

	// 更新已存在 → rows == 1
	rows, err := repo.Update(ctx, &User{ID: "1", Name: "alice-x", Age: 32})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)

	// 更新不存在 → (0, nil)，不报错（幂等）
	rows, err = repo.Update(ctx, &User{ID: "no-such", Name: "ghost", Age: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(0), rows)

	// 更新到"本就是目标值" → MatchedCount 仍为 1（Mongo 语义：匹配即算）
	rows, err = repo.Update(ctx, &User{ID: "1", Name: "alice-x", Age: 32})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)
}

// TestMongo_DeleteIdempotent 锁定 Delete 幂等契约。
func TestMongo_DeleteIdempotent(t *testing.T) {
	repo := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, &User{ID: "1", Name: "alice", Age: 30}))

	rows, err := repo.Delete(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)

	// 删不存在 → (0, nil)
	rows, err = repo.Delete(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "no-such")}})
	require.NoError(t, err)
	assert.Equal(t, int64(0), rows)
}

func protoUint32(v uint32) *uint32 { return &v }
