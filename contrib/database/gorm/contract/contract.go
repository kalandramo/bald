// Package contract 提供 gorm（通用 SQL）后端的契约装配：bootstrapv1.Database
// 的 sql 段 → bald-crud/gorm options 映射 + DatabaseRegistry Provider。
//
// 单独成包的原因：根包保持零契约依赖（纯 SDK 封装），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	gormcrud "github.com/kalandramo/bald-crud/gorm"

	gorm "github.com/kalandramo/bald-database-gorm"
)

// Type 是契约 database 段中 gorm（通用 SQL）后端的段名。
const Type = "sql"

// Provider 按契约 database.sql 段构造 SQL 客户端。
// 返回的 cleanup 由 appkit 停机 Effect 回放（数据库连接最后关）。
// 仅当 database.sql 段存在时被 DatabaseRegistry 调度到。
// 签名与 bootstrap.DatabaseProvider 结构化兼容，直接注册：
//
//	dr := bootstrap.NewDatabaseRegistry()
//	dr.MustRegister(gormcontract.Type, gormcontract.Provider)
func Provider(ctx context.Context, cfg *bootstrapv1.Database) (any, func(), error) {
	sec := cfg.GetSql()
	if sec == nil {
		return nil, nil, fmt.Errorf("database: type=%q but sql section is missing", Type)
	}
	if sec.GetSource() == "" {
		return nil, nil, fmt.Errorf("database: sql.source (DSN) is required")
	}

	opts := buildOptions(ctx, sec)
	client, err := gorm.New(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("database: build sql client: %w", err)
	}
	return client, func() { _ = gorm.Close(client) }, nil
}

// buildOptions 契约段字段 → bald-crud/gorm options（纯函数，可测）。
//
// 未建模的契约/SDK 面（刻意）：
//   - 迁移模型：代码声明（WithAutoMigrate / mixin），契约不承载模型清单；
//   - 读写分离（replica DSN）：契约未建模，暂不支持；
//   - prometheus push / tracing 细节：走 observability-otlp 面，不在此展开。
func buildOptions(ctx context.Context, sec *bootstrapv1.Database_SQL) []gorm.Option {
	var opts []gormcrud.Option
	opts = append(opts, gormcrud.WithContext(ctx))
	if d := sec.GetDriver(); d != "" {
		opts = append(opts, gormcrud.WithDriverName(d))
	}
	opts = append(opts, gormcrud.WithDSN(sec.GetSource()))
	opts = append(opts, gormcrud.WithEnableMigrate(sec.GetMigrate()))
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
