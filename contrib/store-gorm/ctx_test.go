package baldgorm

import (
	"context"
	"errors"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCtx_PropagatedToGorm 锁定 ctx 必须传播到 GORM 操作（原缺陷：gorm.go 所有
// 方法把 ctx 参数写成 `_` 丢弃——请求取消/超时无法传播，DB 操作照常执行）。
//
// 验证方式：传入已取消的 ctx，GORM 应返回 context.Canceled（而非静默成功）。
func TestCtx_PropagatedToGorm(t *testing.T) {
	p := newTestProvider(t)
	repo := store.NewStore[User](p)

	// 先正常建一条，供 Get/Update/Delete 用。
	require.NoError(t, repo.Create(context.Background(), &User{ID: "1", Name: "alice", Age: 30}))
	where := &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}}

	cctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	t.Run("Create", func(t *testing.T) {
		err := repo.Create(cctx, &User{ID: "2", Name: "bob", Age: 20})
		assert.ErrorIs(t, err, context.Canceled, "Create 应把 ctx 传给 GORM")
	})
	t.Run("Get", func(t *testing.T) {
		_, err := repo.Get(cctx, where)
		assert.ErrorIs(t, err, context.Canceled, "Get 应把 ctx 传给 GORM")
	})
	t.Run("List", func(t *testing.T) {
		_, _, err := repo.List(cctx, where)
		assert.ErrorIs(t, err, context.Canceled, "List 应把 ctx 传给 GORM")
	})
	t.Run("Count", func(t *testing.T) {
		_, err := repo.Count(cctx, where)
		assert.ErrorIs(t, err, context.Canceled, "Count 应把 ctx 传给 GORM")
	})
	t.Run("Update", func(t *testing.T) {
		_, err := repo.Update(cctx, &User{ID: "1", Name: "x", Age: 1})
		assert.ErrorIs(t, err, context.Canceled, "Update 应把 ctx 传给 GORM")
	})
	t.Run("Delete", func(t *testing.T) {
		_, err := repo.Delete(cctx, where)
		assert.ErrorIs(t, err, context.Canceled, "Delete 应把 ctx 传给 GORM")
	})
}

// TestCtx_TimeoutPropagated 锁定超时传播（WithTimeout 已过期的 ctx）。
func TestCtx_TimeoutPropagated(t *testing.T) {
	p := newTestProvider(t)
	repo := store.NewStore[User](p)

	tctx, cancel := context.WithTimeout(context.Background(), 0) // 立即超时
	defer cancel()

	err := repo.Create(tctx, &User{ID: "1", Name: "x", Age: 1})
	assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled),
		"过期 ctx 应传播为 DeadlineExceeded/Canceled，got %v", err)
}
