package baldgorm

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/pkg/store"
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGorm_UpdateIdempotent 锁定 Update 的**幂等契约**（与 Delete 对称）。
//
// 背景：Update 原先对 `RowsAffected == 0` 返回 store.ErrNotFound——把 SQL 原生
// 「更新 0 行是正常执行」误判为错误。现改为返回受影响行数：0 行 = (0, nil)。
// 更新的目标是让数据变成目标值，本来就是目标值，操作本身成功。
//
// 需要「必须存在才能更新」的调用方，判 rows == 0 自行实现（无需额外 Get 往返）。
func TestGorm_UpdateIdempotent(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()
	repo := store.NewStore[User](p)
	require.NoError(t, repo.Create(ctx, &User{ID: "1", Name: "alice", Age: 30}))

	// 1) 更新已存在 → rows == 1，无错。
	rows, err := repo.Update(ctx, &User{ID: "1", Name: "alice-x", Age: 32})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)
	got, err := repo.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	require.NoError(t, err)
	assert.Equal(t, "alice-x", got.Name)
	assert.Equal(t, 32, got.Age)

	// 2) **核心**：更新不存在 → (0, nil)，不报 ErrNotFound（幂等）。
	rows, err = repo.Update(ctx, &User{ID: "no-such", Name: "ghost", Age: 1})
	require.NoError(t, err, "update missing must be idempotent: want nil (幂等语义)")
	assert.Equal(t, int64(0), rows)

	// 3) 更新到"本就是目标值" → 仍是成功执行（面向最终状态，非过程）。
	rows, err = repo.Update(ctx, &User{ID: "1", Name: "alice-x", Age: 32})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)

	// 4) 「必须存在才能更新」的上层显式实现：判 rows == 0。
	rows, err = repo.Update(ctx, &User{ID: "gone", Name: "x", Age: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(0), rows)
}
