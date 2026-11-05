// Package contract 提供 gorm（通用 SQL）后端的契约装配：bootstrapv1.Database
// 的 sql 段 → bald-crud/gorm options 映射 + 构造 func（供段驱动 Provider 的
// Build 复用）。
//
// 单独成包的原因：根包保持零契约依赖（纯 SDK 封装），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	gormcrud "github.com/kalandramo/bald-crud/gorm"

	gorm "github.com/kalandramo/bald/contrib/database/gorm"
)

// Type 是契约 database 段中 gorm（通用 SQL）后端的段名。
const Type = "sql"

// allowedDrivers 是 bald-crud/gorm 的 createDriver 认识的驱动名。
//
// 这张表存在的原因：SDK 那个 switch 写成 `default: fallthrough` 到 sqlite，
// 未识别的名字（含拼错的 "postgress"）不报错，而是**静默开一个本地 sqlite**——
// 契约声明的驱动与实际用的驱动可以完全不同名而无人知晓。
var allowedDrivers = []string{
	"mysql", "postgres", "clickhouse", "sqlserver", "bigquery", "gaussdb",
	"sqlite", "go_sqlite",
}

// Provider 按契约 database.sql 段构造 SQL 客户端。
// 返回的 cleanup 由 appkit 停机 Effect 回放（数据库连接最后关）。
// 供自定义 Provider 的 Build 直接调用。
//
// ctx 不投递给客户端：bald-crud/gorm v0.3.0 的 `Client.ctx` 只有一个写入方
// （WithContext），没有任何读取方——把它传进去等于给一个没人看的字段赋值。
func Provider(ctx context.Context, cfg *bootstrapv1.Database) (any, func(), error) {
	sec := cfg.GetSql()
	if sec == nil {
		return nil, nil, fmt.Errorf("database: type=%q but sql section is missing", Type)
	}
	// 驱动名先判：把「枚举合法」排在「不透明串非空」之前，合法集才能被判据逐个过一遍
	// 而不触真连（见 contract_test.go 的 TestAllowedDriversTableShape）。
	d := sec.GetDriver()
	if d == "" {
		return nil, nil, fmt.Errorf("database: sql.driver is required, one of [%s]", strings.Join(allowedDrivers, " "))
	}
	if !slices.Contains(allowedDrivers, d) {
		return nil, nil, fmt.Errorf("database: sql.driver=%q is not supported (SDK would silently fall through to sqlite), one of [%s]",
			d, strings.Join(allowedDrivers, " "))
	}
	if sec.GetSource() == "" {
		return nil, nil, fmt.Errorf("database: sql.source (DSN) is required")
	}

	opts := buildOptions(sec)
	client, err := gorm.New(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("database: build sql client: %w", err)
	}
	return client, func() { _ = gorm.Close(client) }, nil
}

// buildOptions 契约段字段 → bald-crud/gorm options（纯函数，可测）。
//
// 未建模的契约/SDK 面（刻意）：
//   - 迁移模型清单：契约不承载，代码声明（RegisterMigrateModels / WithAutoMigrate）；
//   - 读写分离（replica DSN）：契约未建模，暂不支持；
//   - prometheus push / tracing 细节：走 observability-otlp 面，不在此展开。
func buildOptions(sec *bootstrapv1.Database_SQL) []gorm.Option {
	var opts []gormcrud.Option
	opts = append(opts, gormcrud.WithDriverName(sec.GetDriver()))
	opts = append(opts, gormcrud.WithDSN(sec.GetSource()))
	opts = append(opts, gormcrud.WithEnableMigrate(sec.GetMigrate()))
	// SDK 的 initGormClient 在 enableMigrate 时直接**调用** Client.getMigrateModels
	// 这个字段（v0.3.0 client.go:314，无 nil 检查），而本包的构造路径不接受业务
	// 注入它——于是契约写 `migrate: true` 的服务在构造期当场 panic。顶一个返回空
	// 的 getter 挡住这条；真正干活的是 NewClient 里第二次 pass
	// （resolveMigrateModels：全局注册 + 实例 migrateModels），由同一个
	// enableMigrate 门住，语义不变。
	opts = append(opts, gormcrud.WithGetMigrateModels(func() []any { return nil }))
	opts = append(opts, gormcrud.WithEnableTrace(sec.GetEnableTrace()))
	opts = append(opts, gormcrud.WithEnableMetrics(sec.GetEnableMetrics()))
	if n := int(sec.GetMaxIdleConnections()); n > 0 {
		opts = append(opts, gormcrud.WithMaxIdleConns(n))
	}
	if n := int(sec.GetMaxOpenConnections()); n > 0 {
		opts = append(opts, gormcrud.WithMaxOpenConns(n))
	}
	if s := sec.GetConnectionMaxLifetimeSeconds(); s > 0 {
		opts = append(opts, gormcrud.WithConnMaxLifetime(time.Duration(s)*time.Second))
	}
	return opts
}
