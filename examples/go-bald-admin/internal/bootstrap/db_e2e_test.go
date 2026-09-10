package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// dsnScheme 的 scheme 推断测试已随实现上移 contrib/store-gorm（conn_test.go TestDSNScheme）。

// TestOpenDB_UnsupportedScheme 验证未知驱动的 DSN 返回明确错误（不静默降级）。
// 真实 postgres/mysql DSN 在本地无数据库环境无法连通，仅校验分流前的预检失败路径。
func TestOpenDB_UnsupportedScheme(t *testing.T) {
	t.Setenv("BALD_ADMIN_DB_DSN", "oracle://u:p@h:1521/db")
	_, err := openDB()
	assert.Error(t, err, "未知 scheme 必须报错")
}
