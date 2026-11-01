package baldgorm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// tenantUser 带租户列，供 Update 隔离测试。
type tenantUser struct {
	ID       string `gorm:"primaryKey"`
	TenantID string
	Name     string
}

// TestGorm_UpdateIsolation 锁定：Update 的隔离谓词与主键条件 AND 连接——
// 更新他租户记录影响 0 行且数据不变。
func TestGorm_UpdateIsolation(t *testing.T) {
	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
	t.Cleanup(func() { store.UnregisterTenant("tenant_id") })

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&tenantUser{}))

	repo := store.NewStore[tenantUser](NewGormProvider[tenantUser](db, func(u *tenantUser) string { return u.ID }))
	bg := context.Background()
	require.NoError(t, repo.Create(bg, &tenantUser{ID: "1", TenantID: "t-1", Name: "a"}))
	require.NoError(t, repo.Create(bg, &tenantUser{ID: "2", TenantID: "t-2", Name: "b"}))

	t1 := contextx.WithTenantID(bg, "t-1")

	rows, err := repo.Update(t1, &tenantUser{ID: "2", Name: "hijacked"})
	require.NoError(t, err)
	assert.Zero(t, rows, "跨租户 Update 应影响 0 行")

	got, err := repo.Get(bg, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "2")}})
	require.NoError(t, err)
	assert.Equal(t, "b", got.Name, "他租户记录不得被改写")
	assert.Equal(t, "t-2", got.TenantID, "他租户记录归属不得被迁移")

	rows, err = repo.Update(t1, &tenantUser{ID: "1", Name: "a2"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)

	got, err = repo.Get(bg, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	require.NoError(t, err)
	assert.Equal(t, "a2", got.Name)
	assert.Equal(t, "t-1", got.TenantID)
}

// TestGorm_UpdateDataScopeIsolation 锁定：数据范围策略与租户谓词一同进入
// Update 的 WHERE——同租户但越出范围的记录影响 0 行；写路径仍覆写实体租户。
func TestGorm_UpdateDataScopeIsolation(t *testing.T) {
	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
	t.Cleanup(func() { store.UnregisterTenant("tenant_id") })

	// SELF 范围：只允许访问 id 等于当前主体标识的行。
	dispose := store.RegisterDataScopeWithDisposer(
		func(_ context.Context, claims *authn.AuthClaims) []*storev1.FilterCondition {
			if claims == nil || claims.Subject == "" {
				return nil
			}
			return []*storev1.FilterCondition{store.Eq("id", claims.Subject)}
		})
	t.Cleanup(dispose)

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&tenantUser{}))

	repo := store.NewStore[tenantUser](NewGormProvider[tenantUser](db, func(u *tenantUser) string { return u.ID }))
	bg := context.Background()
	require.NoError(t, repo.Create(bg, &tenantUser{ID: "1", TenantID: "t-1", Name: "a"}))
	require.NoError(t, repo.Create(bg, &tenantUser{ID: "2", TenantID: "t-1", Name: "b"}))

	self := authn.ContextWithAuthClaims(bg, &authn.AuthClaims{Subject: "1", TenantID: "t-1"})

	rows, err := repo.Update(self, &tenantUser{ID: "2", Name: "hijacked"})
	require.NoError(t, err)
	assert.Zero(t, rows, "越出数据范围的 Update 应影响 0 行")

	got, err := repo.Get(bg, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "2")}})
	require.NoError(t, err)
	assert.Equal(t, "b", got.Name, "范围外记录不得被改写")

	rows, err = repo.Update(self, &tenantUser{ID: "1", Name: "a2"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)

	// 写路径覆写：实体自带的他租户值被 ctx 租户覆盖，隔离不被迁移绕过。
	rows, err = repo.Update(self, &tenantUser{ID: "1", TenantID: "t-2", Name: "a3"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)

	got, err = repo.Get(bg, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	require.NoError(t, err)
	assert.Equal(t, "a3", got.Name)
	assert.Equal(t, "t-1", got.TenantID)
}
