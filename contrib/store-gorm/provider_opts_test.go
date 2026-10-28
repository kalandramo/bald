package baldgorm

// provider_opts_test.go —— AppProvider 的变参 Option 透传 Open 生效的钉子。
//   1. 零变参（nil opts）行为与透传前完全一致：空 Source 段按「未设置」处理，
//      落缺省 SQLite 内存库；
//   2. WithEnv 透传生效：env 命中即覆盖契约段 Source（如 bald-admin 的
//      BALD_ADMIN_DB_DSN）；
//   3. 业务 opts 在 WithConfig 之后应用，可覆盖契约段（WithDSN > Source）；
//      Apply 落声明不丢透传的 Option。

import (
	"context"
	"path/filepath"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// buildAppProvider 包装 AppProvider 产物 Build：断言产物形状并注册连接池清理。
func buildAppProvider(t *testing.T, p appkit.Provider, sec *bootstrapv1.Database_SQL) *gorm.DB {
	t.Helper()
	scope := appkit.ApplyScope{Config: &bootstrapv1.BootstrapConfig{
		Database: &bootstrapv1.Database{Sql: sec},
	}}
	product, cleanup, err := p.Build(context.Background(), scope, appkit.Node{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cleanup(context.Background()) })
	m, ok := product.(map[string]any)
	require.True(t, ok, "product type = %T, want map[string]any", product)
	db, ok := m["sql"].(*gorm.DB)
	require.True(t, ok, "product[sql] type = %T, want *gorm.DB", m["sql"])
	return db
}

// TestAppProvider_NilOptsUnchanged 零变参：与透传前行为完全一致。
func TestAppProvider_NilOptsUnchanged(t *testing.T) {
	db := buildAppProvider(t, AppProvider(), &bootstrapv1.Database_SQL{})
	require.NoError(t, db.AutoMigrate(&User{}))
	require.NoError(t, db.Create(&User{ID: "prov-nil", Name: "default-memory"}).Error)

	var got User
	require.NoError(t, db.First(&got, "id = ?", "prov-nil").Error)
	assert.Equal(t, "default-memory", got.Name)
}

// TestAppProvider_WithEnvPassthrough WithEnv 透传生效：env 覆盖契约段 Source。
// 契约段故意指向未注册驱动 postgres——不透传（或透传不生效）时 Build 必败。
func TestAppProvider_WithEnvPassthrough(t *testing.T) {
	fileA := filepath.Join(t.TempDir(), "a.db")
	seed := openTestDB(t, WithDSN(fileA), WithDriver("sqlite"))
	require.NoError(t, seed.AutoMigrate(&User{}))
	require.NoError(t, seed.Create(&User{ID: "prov-env", Name: "from-env"}).Error)

	t.Setenv("BALDGORM_TEST_APP_DSN", fileA)
	sec := &bootstrapv1.Database_SQL{Driver: "postgres", Source: "host=from-config"}

	// 反证：零变参沿契约段打开 postgres，未注册驱动 fail-fast。
	_, _, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{Database: &bootstrapv1.Database{Sql: sec}},
	}, appkit.Node{})
	require.ErrorContains(t, err, "unregistered database driver")

	db := buildAppProvider(t,
		AppProvider(WithEnv("BALDGORM_TEST_APP_DSN"), WithDriver("sqlite")), sec)
	var got User
	require.NoError(t, db.First(&got, "id = ?", "prov-env").Error)
	assert.Equal(t, "from-env", got.Name)
}

// TestAppProvider_WithDSNPassthrough 业务 opts 在 WithConfig 之后应用：可覆盖契约段。
func TestAppProvider_WithDSNPassthrough(t *testing.T) {
	fileB := filepath.Join(t.TempDir(), "b.db")
	seed := openTestDB(t, WithDSN(fileB), WithDriver("sqlite"))
	require.NoError(t, seed.AutoMigrate(&User{}))
	require.NoError(t, seed.Create(&User{ID: "prov-dsn", Name: "from-dsn"}).Error)

	sec := &bootstrapv1.Database_SQL{Driver: "postgres", Source: "host=from-config"}
	db := buildAppProvider(t,
		AppProvider(WithDSN(fileB), WithDriver("sqlite")), sec)

	var got User
	require.NoError(t, db.First(&got, "id = ?", "prov-dsn").Error)
	assert.Equal(t, "from-dsn", got.Name)
}

// TestAppProvider_ApplyKeepsOpts Apply 落声明不丢透传的 Option（cron 同款钉子）。
func TestAppProvider_ApplyKeepsOpts(t *testing.T) {
	p := AppProvider(WithEnv("BALDGORM_TEST_APPLY"))
	d := &appkit.Decl{}
	p.Apply(d)
	require.Len(t, d.Providers, 1)
	gp, ok := d.Providers[0].(gormProvider)
	require.True(t, ok, "decl provider type = %T, want gormProvider", d.Providers[0])
	require.Len(t, gp.opts, 1)
}
