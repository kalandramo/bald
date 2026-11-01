package baldmongo

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tenantUser 带租户字段，供 Update 隔离测试。
type tenantUser struct {
	ID       string `bson:"id"`
	TenantID string `bson:"tenant_id"`
	Name     string `bson:"name"`
}

// TestMongo_UpdateIsolation 锁定：Update 的隔离谓词与主键条件 AND 连接——
// 更新他租户文档匹配 0 个且数据不变。
func TestMongo_UpdateIsolation(t *testing.T) {
	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
	t.Cleanup(func() { store.UnregisterTenant("tenant_id") })

	db := newTestDB(t)
	p := NewMongoProvider[tenantUser](db, "tenant_users", func(u *tenantUser) string { return u.ID })
	require.NoError(t, p.Migrate(context.Background()))
	repo := store.NewStore[tenantUser](p)
	bg := context.Background()
	require.NoError(t, repo.Create(bg, &tenantUser{ID: "1", TenantID: "t-1", Name: "a"}))
	require.NoError(t, repo.Create(bg, &tenantUser{ID: "2", TenantID: "t-2", Name: "b"}))

	t1 := contextx.WithTenantID(bg, "t-1")

	rows, err := repo.Update(t1, &tenantUser{ID: "2", Name: "hijacked"})
	require.NoError(t, err)
	assert.Zero(t, rows, "跨租户 Update 应匹配 0 个")

	got, err := repo.Get(bg, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "2")}})
	require.NoError(t, err)
	assert.Equal(t, "b", got.Name, "他租户文档不得被改写")
	assert.Equal(t, "t-2", got.TenantID, "他租户文档归属不得被迁移")

	rows, err = repo.Update(t1, &tenantUser{ID: "1", Name: "a2"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)

	got, err = repo.Get(bg, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	require.NoError(t, err)
	assert.Equal(t, "a2", got.Name)
	assert.Equal(t, "t-1", got.TenantID)
}
