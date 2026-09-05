package baldgorm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kalandramo/bald/pkg/store"
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// User 是测试实体（gorm 模型）。
type User struct {
	ID   string `gorm:"primaryKey"`
	Name string
	Age  int
}

// newTestProvider 每个测试用独立临时数据库文件，避免内存库跨测试串扰。
func newTestProvider(t *testing.T) *Provider[User] {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	p := NewGormProvider[User](db, func(u *User) string { return u.ID })
	require.NoError(t, p.Migrate(context.Background()))
	return p
}

func TestGormCRUD(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()
	repo := store.NewStore[User](p)

	// Create
	require.NoError(t, repo.Create(ctx, &User{ID: "1", Name: "alice", Age: 30}))
	require.NoError(t, repo.Create(ctx, &User{ID: "2", Name: "bob", Age: 25}))
	// 冲突
	assert.ErrorIs(t, repo.Create(ctx, &User{ID: "1", Name: "dup", Age: 1}), store.ErrConflict)

	// Get
	got, err := repo.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	require.NoError(t, err)
	assert.Equal(t, "alice", got.Name)

	// Update
	require.NoError(t, repo.Update(ctx, &User{ID: "1", Name: "alice2", Age: 31}))
	got, _ = repo.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	assert.Equal(t, "alice2", got.Name)
	assert.Equal(t, 31, got.Age)

	// Count
	n, err := repo.Count(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	// Delete
	require.NoError(t, repo.Delete(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "2")}}))
	_, err = repo.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "2")}})
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestGormFilterSortPaging(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()
	repo := store.NewStore[User](p)

	for i, u := range []User{
		{ID: "1", Name: "alice", Age: 30},
		{ID: "2", Name: "bob", Age: 25},
		{ID: "3", Name: "carol", Age: 35},
		{ID: "4", Name: "dave", Age: 25},
	} {
		_ = i
		require.NoError(t, repo.Create(ctx, &u))
	}

	// 过滤：age >= 25 且 name LIKE %a%
	res, err := repo.ListWithPaging(ctx, &storev1.PagingRequest{
		FilterExpr: &storev1.FilterExpr{
			Conditions: []*storev1.FilterCondition{
				store.Gte("age", "25"),
				store.Contains("name", "a"),
			},
		},
		Sorting: []*storev1.Sorting{store.SortDesc("age")},
	})
	require.NoError(t, err)
	// 命中：alice(30), carol(35), dave(25) 三名均含 "a"；bob 不含
	require.Len(t, res.Items, 3)
	assert.Equal(t, "carol", res.Items[0].Name) // 35
	assert.Equal(t, "alice", res.Items[1].Name) // 30
	assert.Equal(t, "dave", res.Items[2].Name)  // 25
	assert.Equal(t, uint64(3), res.Meta.GetTotal().GetValue())

	// 分页：第 1 页，每页 2
	res, err = repo.ListWithPaging(ctx, &storev1.PagingRequest{
		Page:      protoUint32(1),
		PageSize:  protoUint32(2),
		Sorting:   []*storev1.Sorting{store.Sort("age")},
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 2)
	assert.Equal(t, "bob", res.Items[0].Name)   // 25
	assert.Equal(t, "dave", res.Items[1].Name)  // 25 (稳定序)
	assert.Equal(t, uint32(1), res.Meta.GetCurrentPage().GetValue())
	assert.Equal(t, uint32(2), res.Meta.GetPageSize())
	assert.Equal(t, uint32(2), res.Meta.GetTotalPages().GetValue())
	assert.Equal(t, uint64(4), res.Meta.GetTotal().GetValue())
}

func protoUint32(v uint32) *uint32 { return &v }
