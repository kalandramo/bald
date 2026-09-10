// testutil.go 测试助手（依赖标准库 testing，故与生产逻辑 conn.go 分文件）。
package baldgorm

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"
)

// NewTestDB 返回测试用 SQLite 临时文件库，测试结束自动关闭连接池
//（Windows 下未释放的 sqlite 文件句柄会导致 TempDir 清理失败，
//与 go-bald-admin 同款坑）。每个测试独立库文件，无跨测试串扰。
func NewTestDB(t testing.TB) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "baldgorm-test.db")
	db, err := Open(WithDSN(dbPath), WithDriver("sqlite"))
	if err != nil {
		t.Fatalf("baldgorm: open test db: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}
