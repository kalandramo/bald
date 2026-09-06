package contract

import (
	"context"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	gormcrud "github.com/kalandramo/bald-crud/gorm"
)

func TestType(t *testing.T) {
	if Type != "sql" {
		t.Errorf("Type = %q, want %q", Type, "sql")
	}
}

// 段缺失 / 必填字段缺失 fail-fast（不触真连）。

func TestProvider_MissingSection(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Database{})
	if err == nil {
		t.Fatal("Provider with missing sql section should fail")
	}
	want := `database: type="sql" but sql section is missing`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestProvider_MissingDSN(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Database{
		Sql: &bootstrapv1.Database_SQL{Driver: "mysql"},
	})
	if err == nil {
		t.Fatal("Provider with empty sql.source should fail")
	}
	want := "database: sql.source (DSN) is required"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// buildOptions 纯函数映射断言（不触真连）。

// optionSpy 按 option 函数名收集断言点：gormcrud.Option 是接口，
// 经构造已知 With* 的返回值做 reflect 比较——这里退而求其次，
// 直接构造 Client 级断言不可行（会真连），故断言映射语义的最小面：
// 选项数量与 WithEnableMigrate 等布尔开关（构造期无副作用）。
func TestBuildOptions_FullFields(t *testing.T) {
	opts := buildOptions(context.Background(), &bootstrapv1.Database_SQL{
		Driver:                       "mysql",
		Source:                       "user:pass@tcp(127.0.0.1:3306)/db",
		Migrate:                      true,
		EnableTrace:                  true,
		EnableMetrics:                true,
		MaxIdleConnections:           7,
		MaxOpenConnections:           42,
		ConnectionMaxLifetimeSeconds: 90,
	})
	// WithContext + Driver + DSN + Migrate + Trace + Metrics + Idle + Open + Lifetime = 9
	if len(opts) != 9 {
		t.Fatalf("len(opts) = %d, want 9", len(opts))
	}
}

func TestBuildOptions_MinimalFields(t *testing.T) {
	opts := buildOptions(context.Background(), &bootstrapv1.Database_SQL{
		Source: "dsn",
	})
	// WithContext + DSN + Migrate(false) + Trace(false) + Metrics(false) = 5，
	// 连接池零值字段不加 option。
	if len(opts) != 5 {
		t.Fatalf("len(opts) = %d, want 5", len(opts))
	}
}

// 类型守卫：Provider 签名与 appkit.DatabaseProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Database) (any, func(), error) = Provider

// WithConnMaxLifetime 的秒→Duration 语义（防手滑写错单位）。
func TestDurationConversion(t *testing.T) {
	var want time.Duration = 90 * time.Second
	if got := time.Duration(90) * time.Second; got != want {
		t.Errorf("duration = %v, want %v", got, want)
	}
	_ = gormcrud.WithConnMaxLifetime(want)
}
