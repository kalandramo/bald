package baldgorm

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGormDeleteIdempotent 锁定 Delete 的**幂等契约**（2026-09-23 决策，与
// inmemory 侧对称）。0 行匹配返回 (0, nil) 而非 ErrNotFound——两 provider 行为一致。
func TestGormDeleteIdempotent(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()
	repo := store.NewStore[User](p)

	require.NoError(t, repo.Create(ctx, &User{ID: "1", Name: "alice", Age: 30}))

	// 1) 删已存在 → rows == 1。
	rows, err := repo.Delete(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)

	// 2) **核心**：删不存在 → (0, nil)，不报 ErrNotFound。
	rows, err = repo.Delete(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "no-such")}})
	require.NoError(t, err, "delete missing must be idempotent (0, nil)")
	assert.Equal(t, int64(0), rows)

	// 3) 集合删除命中多行 → rows == 命中数。
	for _, u := range []*User{{ID: "2", Name: "b"}, {ID: "3", Name: "c"}} {
		require.NoError(t, repo.Create(ctx, u))
	}
	rows, err = repo.Delete(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.In("id", "2", "3")}})
	require.NoError(t, err)
	assert.Equal(t, int64(2), rows)
}
