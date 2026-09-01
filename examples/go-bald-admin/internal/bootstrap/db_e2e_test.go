package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDSNScheme 验证 M6 外部 DB 分流的 scheme 解析（postgres/mysql/其他/空）。
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

// TestOpenDB_UnsupportedScheme 验证未知 scheme 的 DSN 返回明确错误（不静默降级）。
// 真实 postgres/mysql DSN 在本地无数据库环境无法连通，仅校验分流前的预检失败路径。
func TestOpenDB_UnsupportedScheme(t *testing.T) {
	t.Setenv("BALD_ADMIN_DB_DSN", "oracle://u:p@h:1521/db")
	_, err := openDB()
	assert.Error(t, err, "未知 scheme 必须报错")
}
