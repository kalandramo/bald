// conn.go 提供 *gorm.DB 的装配入口（Open）与驱动注册表（RegisterDialector），
// 把「快速开发不引入真实依赖」的缺省行为收敛为框架能力：
//
//	db, _ := baldgorm.Open()                                  // 零 Option：SQLite 内存库，clone 即跑
//	db, _ := baldgorm.Open(                                   // 生产：契约段直装（bconf database.sql）
//	    baldgorm.WithConfig(cfg.GetDatabase().GetSql()),
//	)
//	db, _ := baldgorm.Open(                                   // CI 注入：env 覆盖 DSN
//	    baldgorm.WithEnv("APP_DB_DSN"),
//	    baldgorm.WithConfig(cfg.GetDatabase().GetSql()),
//	)
//
// 驱动策略（对齐 registry 模块的注册制，未 import 的后端零依赖）：
//   - sqlite：预注册（glebarez 纯 Go driver，本模块既有直接依赖），别名 sqlite/sqlite3/file；
//   - postgres / mysql 等：业务侧 import gorm driver 后显式注册：
//
//	import gormpostgres "gorm.io/driver/postgres"
//
//	baldgorm.RegisterDialector("postgres", gormpostgres.Open)
//	baldgorm.RegisterDialector("postgresql", gormpostgres.Open) // URL 形式 scheme 别名
//
// 未注册的驱动 Open 报明确错误（fail-fast，不静默降级）。
package baldgorm

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DialectorFactory 按连接串构造 gorm.Dialector——签名与 sqlite.Open /
// postgres.Open / mysql.Open 一致，可直接传驱动包的 Open 函数。
type DialectorFactory func(dsn string) gorm.Dialector

var (
	dialectMu sync.RWMutex
	dialects  = map[string]DialectorFactory{}
)

func init() {
	// sqlite 是缺省引擎：预注册（driver 已是本模块直接依赖，不新增依赖面）。
	for _, name := range []string{"sqlite", "sqlite3", "file"} {
		RegisterDialector(name, sqlite.Open)
	}
}

// RegisterDialector 注册数据库驱动（全局、幂等覆盖、大小写不敏感）。
// 并发安全；典型调用点在业务包 init 或 bootstrap 装配期。
func RegisterDialector(name string, open DialectorFactory) {
	if open == nil {
		panic("baldgorm: RegisterDialector: open must not be nil")
	}
	dialectMu.Lock()
	defer dialectMu.Unlock()
	dialects[normalizeDriver(name)] = open
}

func normalizeDriver(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func lookupDialector(name string) (DialectorFactory, bool) {
	dialectMu.RLock()
	defer dialectMu.RUnlock()
	f, ok := dialects[normalizeDriver(name)]
	return f, ok
}

// options 是 Open 的可选项聚合。
type options struct {
	dsn     string
	driver  string
	envName string
	cfg     *bootstrapv1.Database_SQL
	gormCfg *gorm.Config
}

// Option 是 Open 的可选项。
type Option func(*options)

// WithDSN 显式指定连接串（key=value 形式 DSN 无 scheme，须配合 WithDriver）。
func WithDSN(dsn string) Option { return func(o *options) { o.dsn = dsn } }

// WithDriver 显式指定驱动名，优先于 DSN scheme 推断。
func WithDriver(driver string) Option { return func(o *options) { o.driver = driver } }

// WithEnv 指定覆盖 DSN 的环境变量名（env > WithDSN > WithConfig.Source，
// 便于 CI 注入而不改配置文件；env 只覆盖连接串，驱动沿用其他来源）。
func WithEnv(name string) Option { return func(o *options) { o.envName = name } }

// WithConfig 消费 bconf 契约段 database.sql：driver + source + 连接池参数。
// Source 为空的段忽略（等价未设置）；migrate 保持开关语义——模型列表是业务
// 知识，AutoMigrate 的执行留在业务 bootstrap（框架不越界）。
func WithConfig(cfg *bootstrapv1.Database_SQL) Option {
	return func(o *options) { o.cfg = cfg }
}

// WithGormConfig 整体覆盖 gorm.Config（高级逃生门；缺省为静默 logger，
// 与示例项目历史行为一致）。
func WithGormConfig(cfg *gorm.Config) Option { return func(o *options) { o.gormCfg = cfg } }

// Open 装配 *gorm.DB。
//
// 解析优先级——连接串：WithEnv 指定的环境变量 > WithDSN > WithConfig.Source >
// 缺省 SQLite 内存库（快速开发态：真 SQL、真事务、零部署）；驱动：WithDriver >
// WithConfig.Driver > DSN scheme 推断（key=value 形式 DSN 推断不出 scheme，
// 须显式指定，否则报错）。
func Open(opts ...Option) (*gorm.DB, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	dsn, driver := o.dsn, o.driver
	if o.cfg != nil && o.cfg.GetSource() != "" {
		if dsn == "" {
			dsn = o.cfg.GetSource()
		}
		if driver == "" {
			driver = o.cfg.GetDriver()
		}
	}
	if o.envName != "" {
		if v := os.Getenv(o.envName); v != "" {
			dsn = v
		}
	}

	if dsn == "" {
		// 缺省：共享内存库（连接池内多连接可见同一份数据）。
		// 显式指定了非 sqlite 驱动却不给连接串，属装配错误（fail-fast）。
		if d := normalizeDriver(driver); d == "" || d == "sqlite" || d == "sqlite3" || d == "file" {
			dsn, driver = "file::memory:?cache=shared", "sqlite"
		} else {
			return nil, fmt.Errorf("baldgorm: driver %q requires an explicit dsn (empty dsn only defaults to sqlite memory)", driver)
		}
	}
	if driver == "" {
		driver = dsnScheme(dsn)
	}

	open, ok := lookupDialector(driver)
	if !ok {
		return nil, fmt.Errorf("baldgorm: unregistered database driver %q (dsn scheme %q): "+
			"import the corresponding gorm driver and call baldgorm.RegisterDialector(%q, <driver>.Open)",
			driver, dsnScheme(dsn), normalizeDriver(driver))
	}

	gormCfg := o.gormCfg
	if gormCfg == nil {
		gormCfg = &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	}
	db, err := gorm.Open(open(dsn), gormCfg)
	if err != nil {
		return nil, fmt.Errorf("baldgorm: open %s database: %w", driver, err)
	}

	if o.cfg != nil {
		applyPool(db, o.cfg)
	}
	return db, nil
}

// applyPool 消费契约段连接池参数（>0 才设置；零值=未设置，保留 database/sql 缺省）。
func applyPool(db *gorm.DB, cfg *bootstrapv1.Database_SQL) {
	sqlDB, err := db.DB()
	if err != nil {
		return // 无连接池的 dialector 形态，忽略
	}
	if n := cfg.GetMaxIdleConnections(); n > 0 {
		sqlDB.SetMaxIdleConns(int(n))
	}
	if n := cfg.GetMaxOpenConnections(); n > 0 {
		sqlDB.SetMaxOpenConns(int(n))
	}
	if s := cfg.GetConnectionMaxLifetimeSeconds(); s > 0 {
		sqlDB.SetConnMaxLifetime(time.Duration(s) * time.Second)
	}
}

// dsnScheme 返回连接串的 scheme（驱动分流用）：
//   - "postgres://..." / "mysql://..."      → "postgres" / "mysql"
//   - "file::memory:?cache=shared"（SQLite）→ "file"
//   - 裸 "dbname=test sslmode=disable"      → "dbname"（推断不出驱动，须显式 WithDriver）
//
// 规则：开头到首个 ':' / ' ' / '=' 之前的最短前缀。
func dsnScheme(dsn string) string {
	for i, r := range dsn {
		if r == ':' || r == ' ' || r == '=' {
			return dsn[:i]
		}
	}
	return dsn
}
