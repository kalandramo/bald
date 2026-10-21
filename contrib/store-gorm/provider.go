// provider.go —— 段驱动装配的加法式 Provider 导出（第三方后端集成范本）。
// 既有构造函数（NewGormProvider / Open 等）保留不动；本文件
// 只**新增**对 pkg/appkit 段驱动装配的接入面：AppProvider(opts...) 消费契约
// database.sql 段，经既有 Open(WithConfig, opts...) 打开 *gorm.DB 并落资源容器，
// cleanup 释放连接池。
//
// 接入形态（目标 main.go）：
//
//	appkit.MustServe(ctx,
//	    std.Standard(),
//	    baldgorm.AppProvider(baldgorm.WithEnv("BALD_ADMIN_DB_DSN")),
//	                                            // 契约 database.sql → *gorm.DB（env 覆盖通道；零变参即纯契约轨）
//	    ...) // 域 Wire/业务钩子内经 app.Resource("database.sql") 取用
//
// 驱动解析复用 conn.go 的注册表：sqlite 内置（glebarez 纯 Go，本模块既有
// 依赖），postgres/mysql 等由业务 import 驱动后 RegisterDialector 注册——
// 「未 import 的后端零编译成本、依赖图可见」哲学不变。
package baldgorm

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/pkg/appkit"
)

// AppProvider 返回消费契约 database.sql 段的 appkit.Provider（段路径
// "database.sql"；段缺失即被引擎跳过，合法）。
//
// 命名说明：gorm.go 既有泛型
// Provider[T]（store.DBProvider 的 GORM 实现）占用了 Provider 标识符且原
// 构造函数保留不动，故段驱动装配的导出名取 AppProvider。
//
// Option 透传（transport/cron 等同款先例）：
// opts 原样透传 Open 并在 WithConfig 之后应用——业务 Option（WithEnv /
// WithDSN / WithDriver / WithGormConfig）沿 Open 既有优先级覆盖契约段
// （连接串 env > WithDSN > WithConfig.Source）。AppProvider() 零变参与
// 透传前行为完全一致。典型用法（bald-admin 的 BALD_ADMIN_DB_DSN env）：
//
//	baldgorm.AppProvider(baldgorm.WithEnv("BALD_ADMIN_DB_DSN"))
//
// Build 语义：按契约段（driver/source/连接池参数）经 Open(WithConfig, opts...)
// 打开 *gorm.DB → 产物经引擎 storeProduct 的六资源域约定落资源面（Run 后经
// App.Resource("database.sql") 取用）→ cleanup 关闭连接池
// （停机逆序回放：数据库最后关）。未注册的 driver 沿用 Open 的 fail-fast。
func AppProvider(opts ...Option) appkit.Provider { return gormProvider{opts: opts} }

// gormProvider 消费 database.sql 段；opts 为 AppProvider 透传的业务 Option。
type gormProvider struct{ opts []Option }

func (gormProvider) Segments() []string { return []string{"database.sql"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
// 追加的是携带 opts 的接收者本身——透传的 Option 经 AppProvider 变参构造，
// 落声明时不能丢（transport/cron 同款处置）。
func (p gormProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

// Build 打开契约声明的 SQL 连接并落资源容器。
func (p gormProvider) Build(_ context.Context, s appkit.BuildScope) (any, appkit.Cleanup, error) {
	sec := s.Config.GetDatabase().GetSql()
	if sec == nil {
		return nil, nil, fmt.Errorf("baldgorm: database.sql section is missing " +
			"(provider matched by segment path; report this as a bug)")
	}
	// 透传序：WithConfig 在前、业务 opts 在后——沿 Open 的「后应用者覆盖」
	// 语义，业务 Option 可覆盖契约段；零 opts 时退化为 Open(WithConfig(sec))，
	// 与透传前逐字段等价。
	db, err := Open(append([]Option{WithConfig(sec)}, p.opts...)...)
	if err != nil {
		return nil, nil, err
	}
	return map[string]any{"sql": db}, func(context.Context) error {
		if sqlDB, err := db.DB(); err == nil {
			return sqlDB.Close()
		}
		return nil
	}, nil
}

// 编译期断言：gormProvider 满足 appkit.Provider（含 Option 嵌入面）。
var _ appkit.Provider = gormProvider{}
