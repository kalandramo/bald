package baldgorm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// openTestDB 包装 Open 并注册连接池自动关闭：Windows 下未释放的 sqlite
// 文件句柄会导致 TempDir RemoveAll 失败（与 gorm_test.go 同款处置）。
func openTestDB(t *testing.T, opts ...Option) *gorm.DB {
	t.Helper()
	db, err := Open(opts...)
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// TestDSNScheme 锁定 scheme 推断规则（最短前缀；key=value 裸 DSN 推断不出驱动）。
func TestDSNScheme(t *testing.T) {
	cases := map[string]string{
		"postgres://u:p@h:5432/db":    "postgres",
		"postgresql://u:p@h:5432/db":  "postgresql",
		"mysql://u:p@h:3306/db":       "mysql",
		"file::memory:?cache=shared":  "file",
		"dbname=test sslmode=disable": "dbname",
		"":                            "",
	}
	for dsn, want := range cases {
		assert.Equal(t, want, dsnScheme(dsn), "dsn=%q", dsn)
	}
}

// TestOpen_Default 零 Option：缺省 SQLite 内存库（快速开发态），可直接建表读写。
func TestOpen_Default(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, db.AutoMigrate(&User{}))
	require.NoError(t, db.Create(&User{ID: "1", Name: "alice"}).Error)

	var got User
	require.NoError(t, db.First(&got, "id = ?", "1").Error)
	assert.Equal(t, "alice", got.Name)
}

// TestOpen_ExplicitDriver 显式 DSN + 驱动：临时文件库落盘可查。
func TestOpen_ExplicitDriver(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "explicit.db")
	db := openTestDB(t, WithDSN(dbPath), WithDriver("sqlite"))
	require.NoError(t, db.AutoMigrate(&User{}))
	require.NoError(t, db.Create(&User{ID: "2", Name: "bob"}).Error)

	// 再开一个连接验证落盘（非内存态）。
	db2 := openTestDB(t, WithDSN(dbPath), WithDriver("sqlite"))
	var got User
	require.NoError(t, db2.First(&got, "id = ?", "2").Error)
	assert.Equal(t, "bob", got.Name)
}

// TestOpen_EnvOverride env 优先级：WithEnv 命中的环境变量覆盖 WithDSN（CI 注入场景）。
func TestOpen_EnvOverride(t *testing.T) {
	fileA := filepath.Join(t.TempDir(), "a.db")
	fileB := filepath.Join(t.TempDir(), "b.db")

	dbA := openTestDB(t, WithDSN(fileA), WithDriver("sqlite"))
	require.NoError(t, dbA.AutoMigrate(&User{}))
	require.NoError(t, dbA.Create(&User{ID: "env-a", Name: "from-env"}).Error)

	t.Setenv("BALDGORM_TEST_DSN", fileA)
	db := openTestDB(t, WithEnv("BALDGORM_TEST_DSN"), WithDSN(fileB), WithDriver("sqlite"))

	var got User
	require.NoError(t, db.First(&got, "id = ?", "env-a").Error, "应连接 env 指向的 fileA")
	assert.Equal(t, "from-env", got.Name)
}

// TestOpen_DSNPriority 显式 WithDSN 优先于 WithConfig.Source。
func TestOpen_DSNPriority(t *testing.T) {
	fileA := filepath.Join(t.TempDir(), "a.db")
	fileB := filepath.Join(t.TempDir(), "b.db")

	dbA := openTestDB(t, WithDSN(fileA), WithDriver("sqlite"))
	require.NoError(t, dbA.AutoMigrate(&User{}))
	require.NoError(t, dbA.Create(&User{ID: "dsn-a", Name: "from-dsn"}).Error)

	db := openTestDB(t,
		WithDSN(fileA),
		WithConfig(&bootstrapv1.Database_SQL{Driver: "sqlite", Source: fileB}),
	)

	var got User
	require.NoError(t, db.First(&got, "id = ?", "dsn-a").Error, "WithDSN 应优先于 WithConfig.Source")
	assert.Equal(t, "from-dsn", got.Name)
}

// TestOpen_WithConfig 契约段直装：driver + source 生效，连接池参数消费（>0 才设置）。
func TestOpen_WithConfig(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "config.db")
	cfg := &bootstrapv1.Database_SQL{
		Driver:                       "sqlite",
		Source:                       dbPath,
		MaxIdleConnections:           2,
		MaxOpenConnections:           3,
		ConnectionMaxLifetimeSeconds: 60,
	}
	db := openTestDB(t, WithConfig(cfg))
	require.NoError(t, db.AutoMigrate(&User{}))
	require.NoError(t, db.Create(&User{ID: "3", Name: "carol"}).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	assert.Equal(t, 3, sqlDB.Stats().MaxOpenConnections, "契约段 max_open_connections 应生效")
}

// TestOpen_WithConfigEmptySource 契约段 Source 为空：忽略（等价未设置），回落缺省内存库。
func TestOpen_WithConfigEmptySource(t *testing.T) {
	db := openTestDB(t, WithConfig(&bootstrapv1.Database_SQL{Driver: "postgres", Source: ""}))
	require.NoError(t, db.AutoMigrate(&User{}))
}

// TestOpen_UnregisteredDriver 未注册驱动：fail-fast 明确报错（不静默降级），提示注册方式。
func TestOpen_UnregisteredDriver(t *testing.T) {
	_, err := Open(WithDSN("oracle://u:p@h:1521/db"), WithDriver("oracle"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RegisterDialector", "错误应提示注册入口")
}

// TestOpen_ExplicitDriverWithoutDSN 显式非 sqlite 驱动但缺连接串：装配错误须报错，
// 不得静默回落内存库。
func TestOpen_ExplicitDriverWithoutDSN(t *testing.T) {
	_, err := Open(WithDriver("oracle"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires an explicit dsn")
}

// TestOpen_SchemeFallback 无显式 driver 时按 DSN scheme 推断（file → sqlite 预注册别名）。
func TestOpen_SchemeFallback(t *testing.T) {
	db := openTestDB(t, WithDSN("file::memory:?cache=shared"))
	require.NoError(t, db.AutoMigrate(&User{}))
}

// TestRegisterDialector 注册制：别名（含大小写归一）指向真实引擎后可打开；
// 注册 nil 构造器 panic。
func TestRegisterDialector(t *testing.T) {
	RegisterDialector("MyTest", sqlite.Open) // 混合大小写验证归一化

	dbPath := filepath.Join(t.TempDir(), "alias.db")
	db := openTestDB(t, WithDriver("mytest"), WithDSN(dbPath))
	require.NoError(t, db.AutoMigrate(&User{}))

	// 经 Provider 走一轮完整 Store 语义（注册表驱动与既有桥接正交）。
	p := NewGormProvider[User](db, func(u *User) string { return u.ID })
	repo := store.NewStore[User](p)
	require.NoError(t, repo.Create(context.Background(), &User{ID: "4", Name: "dave"}))
	got, err := repo.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "dave", got.Name)

	assert.Panics(t, func() { RegisterDialector("bad", nil) })
}

// TestNewTestDB 测试助手：独立临时文件库（无串扰）+ cleanup 关闭连接池。
func TestNewTestDB(t *testing.T) {
	db := NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&User{}))
	require.NoError(t, db.Create(&User{ID: "5", Name: "erin"}).Error)

	var got User
	require.NoError(t, db.First(&got, "id = ?", "5").Error)
	assert.Equal(t, "erin", got.Name)
}
