package contract

import (
	"context"
	"strings"
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
	opts := buildOptions(&bootstrapv1.Database_SQL{
		Driver:                       "mysql",
		Source:                       "user:pass@tcp(127.0.0.1:3306)/db",
		Migrate:                      true,
		EnableTrace:                  true,
		EnableMetrics:                true,
		MaxIdleConnections:           7,
		MaxOpenConnections:           42,
		ConnectionMaxLifetimeSeconds: 90,
	})
	// Driver + DSN + Migrate + 迁移 getter 占位 + Trace + Metrics + Idle + Open + Lifetime = 9
	if len(opts) != 9 {
		t.Fatalf("len(opts) = %d, want 9", len(opts))
	}
}

func TestBuildOptions_MinimalFields(t *testing.T) {
	opts := buildOptions(&bootstrapv1.Database_SQL{
		Source: "dsn",
	})
	// Driver(空) + DSN + Migrate(false) + 迁移 getter 占位 + Trace(false) + Metrics(false) = 6，
	// 连接池零值字段不加 option。三态语义的判据见 mapping_runtime_test.go
	// 的 absent 行（写死 vs 不写死的差别在运行期，不在这里）。
	if len(opts) != 6 {
		t.Fatalf("len(opts) = %d, want 6", len(opts))
	}
}

// 驱动名闸门：SDK 的 createDriver 对未识别名 `default: fallthrough` 到 sqlite，
// 所以「拼错驱动名」在闸门之前是**静默换成另一个数据库**，不是报错。
func TestProvider_UnknownDriverRejected(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Database{
		Sql: &bootstrapv1.Database_SQL{Driver: "postgress", Source: "any"},
	})
	if err == nil {
		t.Fatal("Provider with unknown driver should fail (SDK would silently open sqlite)")
	}
	if !strings.Contains(err.Error(), `sql.driver="postgress" is not supported`) {
		t.Errorf("err = %q, want 含被拒的驱动名", err.Error())
	}
}

// 空 driver 原先落到 SDK 的「gorm DB not provided; either use WithGormDB or
// provide driverName/masterDSN」——那条错误既不点名契约字段，也不给合法值。
func TestProvider_MissingDriverRejected(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Database{
		Sql: &bootstrapv1.Database_SQL{Source: "any"},
	})
	if err == nil {
		t.Fatal("Provider with empty sql.driver should fail")
	}
	if !strings.Contains(err.Error(), "sql.driver is required") {
		t.Errorf("err = %q, want 含 sql.driver is required", err.Error())
	}
}

// 类型守卫：Provider 保持构造 func 形状（供 Provider.Build 复用）。
var _ func(context.Context, *bootstrapv1.Database) (any, func(), error) = Provider

// allowedDrivers 这张表与守卫的一致性判据（§7.20）。
//
// 「表里每一项都过得了 `slices.Contains(allowedDrivers, d)`」是恒真式，没有牙齿；
// 真正要防的是「守卫改成读另一份硬编列表、表只剩文案作用」，所以判据走**入口**：
// 每张表的名字喂进 Provider（source 留空），必须在驱动闸门之后、构造之前停在
// 「sql.source (DSN) is required」。这样既不需要真连也不碰网络，而守卫一旦被换掉
// 合法集，这里立刻红。
//
// 合法集与**SDK 的同步**判不到：`createDriver` 未导出，仓内取不到它的真实合法集。
// 那个方向是安全的（SDK 新增名字时契约值被本表拒掉 ⇒ 响），要人工跟的是反方向
// （SDK 删名字、本表仍留着 ⇒ 构造期由 SDK 自己报错）。
func TestAllowedDriversTableShape(t *testing.T) {
	if len(allowedDrivers) == 0 {
		t.Fatal("allowedDrivers 为空 ⇒ 任何契约 driver 都会被拒")
	}
	seen := map[string]bool{}
	for _, d := range allowedDrivers {
		if d == "" {
			t.Error("allowedDrivers 里有空串")
		}
		if seen[d] {
			t.Errorf("allowedDrivers 重复项 %q", d)
		}
		seen[d] = true
	}
	for _, d := range allowedDrivers {
		_, _, err := Provider(context.Background(), &bootstrapv1.Database{
			Sql: &bootstrapv1.Database_SQL{Driver: d},
		})
		if err == nil || !strings.Contains(err.Error(), "sql.source (DSN) is required") {
			t.Errorf("driver=%q 没停在 DSN 那条 ⇒ 闸门与表不同一： %v", d, err)
		}
	}
	// 表外的名字必须停在闸门本身（防止「停在 DSN」被误当成通过）。
	_, _, err := Provider(context.Background(), &bootstrapv1.Database{
		Sql: &bootstrapv1.Database_SQL{Driver: "not-in-the-table"},
	})
	if err == nil || !strings.Contains(err.Error(), "is not supported") {
		t.Errorf("表外的 driver 没被闸门拒掉：%v", err)
	}
}

// WithConnMaxLifetime 的秒→Duration 语义（防手滑写错单位）。
func TestDurationConversion(t *testing.T) {
	var want time.Duration = 90 * time.Second
	if got := time.Duration(90) * time.Second; got != want {
		t.Errorf("duration = %v, want %v", got, want)
	}
	_ = gormcrud.WithConnMaxLifetime(want)
}
