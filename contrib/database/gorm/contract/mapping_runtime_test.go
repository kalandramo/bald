package contract

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	gormcrud "github.com/kalandramo/bald-crud/gorm"

	gorm "github.com/kalandramo/bald/contrib/database/gorm"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracing "gorm.io/plugin/opentelemetry/tracing"
)

// 本文件判 `buildOptions` 那九行 `opts = append` 在**运行期**的落点（§7.14 同族）。
// contract_test.go 只有「构造不报错 + 切片长度」两类判据，而长度判据没有牙齿——
// 删一行只是少一个元素，把值接到别的落点长度照样对（§7.18 的 health_check_timeout
// 就是这种）。
//
// 后端是**纯 Go 的 go_sqlite**（`github.com/glebarez/sqlite`，无 CGO、不触网），所以
// 十条腿全离线，直接进 scope=ci 而不给作业加服务容器。前提探针先证实：这条 DSN
// 形状能在临时目录落盘、跨客户端读回同一行（腿 2），所以「判得到」不需要真数据库。
//
// 观测面（没有一个 bald accessor 是为此加的）：
//   - `Config.Dialector.Name()` + 一次真的建表/写/读 —— driver 与 source 两行。
//   - `Migrator().HasTable` —— migrate 那一行（实测契约值只门得住**全局注册**的模型
//     清单；`WithAutoMigrate` 是 mixin，开不开都跑，见下面的 SDK 语义说明）。
//   - `sql.DB.Stats()` —— 三行连接池。上限直读 `MaxOpenConnections`；另两行读
//     「因该上限/该期限而被关掉」的计数器 `MaxIdleClosed` / `MaxLifetimeClosed`，
//     以及「闲置连接到期自己消失」这一自发效应（配一条不设期限的对照，否则可能被
//     环境噪声蒙过）。go1.27.1 的 `DBStats` **没有** `MaxIdleConnections` 字段
//     （实测编译不过），计数器是唯一的路。
//   - 内存 span 记录器 —— enable_trace 那一行（一次真 Create 落出 3 个 span）。
//   - gorm 自己的插件登记表 `Config.Plugins` —— enable_metrics 那一行。默认注册表
//     判不了（实测）：插件把 gauge 注册到**全局** registerer 且 SDK 吞掉重复注册
//     错误，于是同进程里「关」侧的 Gather 仍带着「开」侧留下的族，开/关差集恒空；
//     gauge 的值还要等 `RefreshInterval`（默认 15s）的 ticker，而契约不承载该间隔。
//
// `go_sqlite` 的坑（所有判表存在性/跨客户端读写的腿都绕开）：`:memory:` 是**每条
// 连接一个空库**，所以这些腿一律用 `t.TempDir()` 下的文件 DSN。

// mappingRow 是判方言读写与迁移的模型表。
type mappingRow struct {
	ID   uint
	Name string
}

func (mappingRow) TableName() string { return "mapping_rows" }

// fileDSN 给一条落盘 sqlite DSN（同目录下所有连接看同一个库）。
func fileDSN(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "mapping.db")
}

// newClient 走**真实入口** Provider（映射 + 驱动名闸门 + 构造），不是本地重算映射。
func newClient(t *testing.T, sec *bootstrapv1.Database_SQL) (*gorm.Client, func()) {
	t.Helper()
	client, cleanup, err := Provider(context.Background(), &bootstrapv1.Database{Sql: sec})
	if err != nil {
		t.Fatalf("Provider(driver=%q source=%q migrate=%v trace=%v metrics=%v): %v",
			sec.GetDriver(), sec.GetSource(), sec.GetMigrate(), sec.GetEnableTrace(), sec.GetEnableMetrics(), err)
	}
	c, ok := client.(*gorm.Client)
	if !ok {
		t.Fatalf("Provider 返回 %T，不是 *gorm.Client", client)
	}
	return c, cleanup
}

// 腿 11：driver_name 对**非 sqlite 家族**的落点 —— 各家 DSN 解析器的话术互不相同。
//
// 为什么需要这条：腿 1 只判了 `go_sqlite` 那一个取值，「把驱动名恒传给 SDK」和
// 「按契约选方言」在它眼里是同一种形状。这里给四个网络方言各喂**同一串** junk DSN，
// 每个驱动自己的解析器报出的话术不同，于是 marker 能把「契约写 postgres 但实际拿
// 到 mysql」这一类接错落点的变异钉住。
//
// 四条 marker 都在**解析期**产出，不碰网络（实测：mysql/postgres/clickhouse/bigquery
// 全在 DSN 解析阶段就返回，没有 dial），所以本机开着某个数据库也不会影响判据。
//
// 判不到的两族（如实登记）：
//   - `postgres` 与 `gaussdb` 同形（都用 pgx 的 keyword/value 解析器，marker 一致）
//     ⇒ 这一对分不开；
//   - `sqlite` 与 `go_sqlite` 把 junk 当**文件路径**，直接构造成功（还在工作目录留下
//     一个名叫 junk 的文件），没有错误文案可当 marker ⇒ 这一族只由腿 1 的
//     `Dialector.Name()` 与真实读写判到。
func TestMapping_DriverNameSelectsThatDialectsParseError(t *testing.T) {
	const junk = "bald-probe-junk"
	rows := []struct {
		driver string
		marker string
	}{
		{"mysql", "invalid DSN"},
		{"postgres", "failed to parse as keyword/value"},
		{"clickhouse", "parse dsn address failed"},
		{"bigquery", "expected bigquery://"},
	}
	for _, r := range rows {
		t.Run(r.driver, func(t *testing.T) {
			_, cleanup, err := Provider(context.Background(), &bootstrapv1.Database{
				Sql: &bootstrapv1.Database_SQL{Driver: r.driver, Source: junk},
			})
			if err == nil {
				cleanup()
				t.Fatalf("%s 的 junk DSN 竟然构造成功 ⇒ 没有可判的方言形状", r.driver)
			}
			if !strings.Contains(err.Error(), r.marker) {
				t.Errorf("driver=%q 的错误里没有该方言自己的话术 %q：%v", r.driver, r.marker, err)
			}
		})
	}
}

// newClientWithExtra 同 newClient，但把测试侧的额外 option 接进来（注入 span
// 记录器；真实装配里这些走 observability-otlp 面，契约不承载）。
func newClientWithExtra(t *testing.T, sec *bootstrapv1.Database_SQL, extra ...gorm.Option) (*gorm.Client, func()) {
	t.Helper()
	opts := append(buildOptions(sec), extra...)
	client, err := gorm.New(opts...)
	if err != nil {
		t.Fatalf("gorm.New: %v", err)
	}
	return client, func() { _ = gorm.Close(client) }
}

func sqlDBOf(t *testing.T, c *gorm.Client) *sql.DB {
	t.Helper()
	db, err := c.DB.DB()
	if err != nil {
		t.Fatalf("DB(): %v", err)
	}
	return db
}

// occupyAndRelease 同时借出 n 条连接再全部归还——触发池的收缩计数器。
func occupyAndRelease(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	conns := make([]*sql.Conn, 0, n)
	for i := 0; i < n; i++ {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatalf("借出第 %d 条连接: %v", i+1, err)
		}
		conns = append(conns, conn)
	}
	for i, conn := range conns {
		if err := conn.Close(); err != nil {
			t.Fatalf("归还第 %d 条连接: %v", i+1, err)
		}
	}
}

// 腿 1：driver_name —— 契约的驱动名要真的选出**能用**的方言。
//
// 删掉 `WithDriverName` 那行 ⇒ SDK 的 `NewClient` 当场报「driverName/masterDSN」缺失，
// 这条腿连构造都过不去。
func TestMapping_DriverSelectsUsableDialect(t *testing.T) {
	c, cleanup := newClient(t, &bootstrapv1.Database_SQL{Driver: "go_sqlite", Source: fileDSN(t)})
	defer cleanup()

	if got := c.Config.Dialector.Name(); got != "sqlite" {
		t.Errorf("Dialector.Name() = %q, want %q", got, "sqlite")
	}
	// 方言真的能用：建表 + 写 + 读回。
	if err := c.AutoMigrate(&mappingRow{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if err := c.Create(&mappingRow{Name: "driver"}).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	var got mappingRow
	if err := c.Where("name = ?", "driver").First(&got).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if got.ID == 0 {
		t.Errorf("读回的行主键为 0 ⇒ 没真落库")
	}
}

// 腿 2：source —— DSN 唯一决定「连的是哪个库」，所以判跨客户端读同一个文件，
// 并带一条「换文件就读不到」的反面对照（否则这条观测不认 source）。
func TestMapping_SourceIsTheDSN(t *testing.T) {
	dsn := fileDSN(t)

	c1, cleanup1 := newClient(t, &bootstrapv1.Database_SQL{Driver: "go_sqlite", Source: dsn})
	if err := c1.AutoMigrate(&mappingRow{}); err != nil {
		cleanup1()
		t.Fatalf("AutoMigrate: %v", err)
	}
	if err := c1.Create(&mappingRow{Name: "written-first"}).Error; err != nil {
		cleanup1()
		t.Fatalf("Create: %v", err)
	}
	cleanup1()

	c2, cleanup2 := newClient(t, &bootstrapv1.Database_SQL{Driver: "go_sqlite", Source: dsn})
	defer cleanup2()
	var got mappingRow
	if err := c2.Where("name = ?", "written-first").First(&got).Error; err != nil {
		t.Fatalf("第二个客户端读不到上一行 ⇒ source 没有真的当 DSN 用: %v", err)
	}

	c3, cleanup3 := newClient(t, &bootstrapv1.Database_SQL{Driver: "go_sqlite", Source: fileDSN(t)})
	defer cleanup3()
	if err := c3.Where("name = ?", "written-first").First(&got).Error; err == nil {
		t.Errorf("换成另一个文件的 DSN 仍读到同一行 ⇒ 这条观测与 source 无关，判据不能用")
	}
}

// 腿 3：migrate —— 契约值门住「代码声明的模型清单」建不建表。
//
// 实测的 SDK 语义（bald-crud/gorm v0.3.0）：`WithEnableMigrate` 只门得住
// `NewClient` 里 `resolveMigrateModels` 那一路（全局 `RegisterMigrateModels` +
// 实例 `migrateModels`）；`WithAutoMigrate` 是 mixin，开关开着关着都跑。
// 所以这条腿判全局注册，而不是 WithAutoMigrate——后者会把「契约值没生效」也判成绿。
func TestMapping_MigrateGatesRegisteredModels(t *testing.T) {
	gormcrud.RegisterMigrateModels(&mappingRow{})

	for _, migrate := range []bool{true, false} {
		c, cleanup := newClient(t, &bootstrapv1.Database_SQL{
			Driver:  "go_sqlite",
			Source:  fileDSN(t),
			Migrate: migrate,
		})
		has := c.Migrator().HasTable(&mappingRow{})
		cleanup()
		if has != migrate {
			t.Errorf("database.sql.migrate=%v ⇒ 表存在=%v，两者应当一致", migrate, has)
		}
	}
}

// 腿 4：migrate=true 的**构造本身**（§7.20 的运行期缺陷）。
//
// SDK 的 `initGormClient` 在 enableMigrate 时**调用 `Client.getMigrateModels` 这个
// 字段**（v0.3.0 client.go:314，无 nil 检查）。契约的构造路径不接受业务注入它，
// 于是修复前「`database.sql.migrate: true`」= 启动期 nil pointer panic。
// 这条腿单独存在：删掉 getter 那行必须让它崩，而不是被别的腿顺带盖掉。
func TestMapping_MigrateTrueDoesNotPanicAtConstruction(t *testing.T) {
	c, cleanup := newClient(t, &bootstrapv1.Database_SQL{
		Driver:  "go_sqlite",
		Source:  fileDSN(t),
		Migrate: true,
	})
	defer cleanup()
	if c.DB == nil {
		t.Fatal("migrate=true 构造出的客户端没有 *gorm.DB")
	}
}

// 腿 5：max_idle_connections —— 上限 1、借还 3 条 ⇒ 计数器必须恰好说「关了 2 条」。
func TestMapping_MaxIdleConnectionsCapsIdlePool(t *testing.T) {
	c, cleanup := newClient(t, &bootstrapv1.Database_SQL{
		Driver:             "go_sqlite",
		Source:             fileDSN(t),
		MaxIdleConnections: 1,
		MaxOpenConnections: 9,
	})
	defer cleanup()
	db := sqlDBOf(t, c)

	occupyAndRelease(t, db, 3)

	// 计数器在归还时同步更新，但 Stats 读的是快照——给一个短重试窗只容忍调度，
	// 不容忍「值不对」：终值必须恰好是 2。
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := db.Stats()
		if st.MaxIdleClosed == 2 {
			if st.Idle > 1 {
				t.Errorf("MaxIdleClosed=2 但 Idle=%d > 契约上限 1", st.Idle)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("MaxIdleClosed = %d, want 2（上限 1 + 归还 3 条 ⇒ 多出的 2 条要被立刻关掉）⇒ WithMaxIdleConns 那行没投递",
				st.MaxIdleClosed)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 腿 6：max_open_connections —— 既要读到上限回声，也要它真的封顶。
func TestMapping_MaxOpenConnectionsIsThePoolCeiling(t *testing.T) {
	c, cleanup := newClient(t, &bootstrapv1.Database_SQL{
		Driver:             "go_sqlite",
		Source:             fileDSN(t),
		MaxIdleConnections: 2,
		MaxOpenConnections: 2,
	})
	defer cleanup()
	db := sqlDBOf(t, c)

	if got := db.Stats().MaxOpenConnections; got != 2 {
		t.Fatalf("Stats().MaxOpenConnections = %d, want 2 ⇒ 契约值没投递", got)
	}

	held := make([]*sql.Conn, 0, 2)
	defer func() {
		for _, conn := range held {
			_ = conn.Close()
		}
	}()
	for i := 0; i < 2; i++ {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatalf("占满第 %d 条连接: %v", i+1, err)
		}
		held = append(held, conn)
	}

	granted := make(chan struct{})
	go func() {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Errorf("第 3 条连接: %v", err)
			return
		}
		_ = conn.Close()
		close(granted)
	}()

	select {
	case <-granted:
		t.Fatalf("占满 2 条后第 3 条立刻拿到 ⇒ 上限没封顶（取的是实现默认「不限」）")
	case <-time.After(200 * time.Millisecond):
	}

	if err := held[0].Close(); err != nil {
		t.Fatalf("释放第 1 条: %v", err)
	}
	held = held[1:]
	select {
	case <-granted:
	case <-time.After(5 * time.Second):
		t.Fatalf("释放 1 条后第 3 条仍拿不到")
	}
	if st := db.Stats(); st.WaitCount == 0 {
		t.Errorf("WaitCount = 0, want >0 —— 等待计数是「曾被上限挡住」的直接证据")
	}
}

// 腿 7：connection_max_lifetime_seconds —— 开与关成对（关侧是开侧的前提）。
//
// 闲置连接到期自己消失是这一行唯一的自发可观测效应；没有关侧对照，这条可能
// 被环境噪声（驱动/OS 的连接回收）蒙过去。
func TestMapping_ConnMaxLifetimeDecidesIdleConnExpiry(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		c, cleanup := newClient(t, &bootstrapv1.Database_SQL{
			Driver:                       "go_sqlite",
			Source:                       fileDSN(t),
			MaxIdleConnections:           2,
			ConnectionMaxLifetimeSeconds: 1,
		})
		defer cleanup()
		db := sqlDBOf(t, c)

		if err := db.Ping(); err != nil {
			t.Fatalf("Ping: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if db.Stats().OpenConnections == 0 {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		st := db.Stats()
		t.Fatalf("设了 1s 期限，5s 后仍 Open=%d Idle=%d ⇒ WithConnMaxLifetime 那行没投递",
			st.OpenConnections, st.Idle)
	})

	t.Run("absent", func(t *testing.T) {
		c, cleanup := newClient(t, &bootstrapv1.Database_SQL{
			Driver:             "go_sqlite",
			Source:             fileDSN(t),
			MaxIdleConnections: 2,
		})
		defer cleanup()
		db := sqlDBOf(t, c)

		if err := db.Ping(); err != nil {
			t.Fatalf("Ping: %v", err)
		}
		time.Sleep(2 * time.Second)
		st := db.Stats()
		if st.OpenConnections == 0 {
			t.Errorf("没设期限闲置连接也归零 ⇒ 这条观测与契约字段无关，判据不能用")
		}
		if st.MaxLifetimeClosed != 0 {
			t.Errorf("MaxLifetimeClosed = %d, want 0 ⇒ 零值没走「不加 option」那条路", st.MaxLifetimeClosed)
		}
	})
}

// 腿 8：三行连接池的 absent 语义 —— 契约留空必须是「不投递」，不是「投 0」。
//
// 这条不是「运行期判不了才退而求其次」：`SetMaxIdleConns(0)` 在 Go 里是合法值
// （0 = 一条闲置都不留），所以「写死成 0」在运行期显形——归还 3 条时三条全关，
// `MaxIdleClosed` 从 1 跳到 3。
//
// 1 不是笔误：Go 的**默认** idle 上限是 2，借 3 还 3 在「什么都没投递」时也要关掉
// 1 条，那是默认值的回声，不是契约值的回声。
//
// open 那一维判不了 absent：`SetMaxOpenConns(0)` 的语义正是「不限」，与不投递同形
// （`Stats().MaxOpenConnections` 两种情况都是 0），所以那一行只有腿 6 的「设了值」
// 方向有执行证据。
func TestMapping_AbsentPoolFieldsLeaveDriverDefaults(t *testing.T) {
	c, cleanup := newClient(t, &bootstrapv1.Database_SQL{Driver: "go_sqlite", Source: fileDSN(t)})
	defer cleanup()
	db := sqlDBOf(t, c)

	occupyAndRelease(t, db, 3)

	st := db.Stats()
	if st.MaxIdleClosed != 1 {
		t.Errorf("MaxIdleClosed = %d, want 1（未投递上限 ⇒ 按 Go 默认 idle=2 只关 1 条）"+
			"——投 0 的话三条全关，会是 3", st.MaxIdleClosed)
	}
	if st.Idle != 2 {
		t.Errorf("Idle = %d, want 2（Go 默认留 2 条闲置）", st.Idle)
	}
}

// 腿 9：enable_trace —— 装没装插件，落到一次真 Create 的 span 条数上。
//
// tracer provider 由测试侧注入（契约不承载 provider），但**插件装不装**由契约值决定。
func TestMapping_EnableTraceDecidesSpanEmission(t *testing.T) {
	for _, enable := range []bool{true, false} {
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))

		c, cleanup := newClientWithExtra(t,
			&bootstrapv1.Database_SQL{Driver: "go_sqlite", Source: fileDSN(t), EnableTrace: enable},
			gormcrud.WithTracingOptions(tracing.WithTracerProvider(tp)))
		if err := c.AutoMigrate(&mappingRow{}); err != nil {
			cleanup()
			t.Fatalf("AutoMigrate (trace=%v): %v", enable, err)
		}
		if err := c.Create(&mappingRow{Name: "traced"}).Error; err != nil {
			cleanup()
			t.Fatalf("Create (trace=%v): %v", enable, err)
		}
		spans := rec.Ended()
		cleanup()

		names := make([]string, 0, len(spans))
		for _, s := range spans {
			names = append(names, s.Name())
		}
		t.Logf("enable_trace=%v ⇒ 一次 Create 落 %d 个 span %v", enable, len(spans), names)

		if enable && len(spans) == 0 {
			t.Errorf("enable_trace=true 却落不出 span ⇒ 插件没装上或没接查询链路")
		}
		if !enable && len(spans) != 0 {
			t.Errorf("enable_trace=false 仍落出 %d 个 span ⇒ 这条观测与契约字段无关", len(spans))
		}
	}
}

// 腿 10：enable_metrics —— 落点是 gorm 的插件登记表（判据面见文件头对默认注册表的
// 实测说明）。开与关各判一次，键名是插件自己的 `Name()`。
func TestMapping_EnableMetricsInstallsPrometheusPlugin(t *testing.T) {
	for _, enable := range []bool{true, false} {
		c, cleanup := newClient(t, &bootstrapv1.Database_SQL{
			Driver:        "go_sqlite",
			Source:        fileDSN(t),
			EnableMetrics: enable,
		})
		_, has := c.Config.Plugins["gorm:prometheus"]
		cleanup()

		if has != enable {
			t.Errorf("enable_metrics=%v ⇒ Config.Plugins 含 gorm:prometheus = %v，两者应当一致", enable, has)
		}
	}
}
