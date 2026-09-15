// Package contract 提供 mongodb 后端的契约装配：bootstrapv1.Database
// 的 mongodb 段 → bald-crud/mongodb options 映射 + DatabaseRegistry Provider。
//
// 单独成包的原因：根包保持零契约依赖（纯 SDK 封装），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	mongocrud "github.com/kalandramo/bald-crud/mongodb"

	mongodb "github.com/kalandramo/bald-database-mongodb"
)

// Type 是契约 database 段中 mongodb 后端的段名。
const Type = "mongodb"

// Provider 按契约 database.mongodb 段构造 MongoDB 客户端。
// 返回的 cleanup 由 appkit 停机 Effect 回放（连接最后关）。
// 仅当 database.mongodb 段存在时被 DatabaseRegistry 调度到。
// 签名与 bootstrap.DatabaseProvider 结构化兼容，直接注册：
//
//	dr := bootstrap.NewDatabaseRegistry()
//	dr.MustRegister(mongocontract.Type, mongocontract.Provider)
func Provider(ctx context.Context, cfg *bootstrapv1.Database) (any, func(), error) {
	sec := cfg.GetMongodb()
	if sec == nil {
		return nil, nil, fmt.Errorf("database: type=%q but mongodb section is missing", Type)
	}
	if sec.GetUri() == "" {
		return nil, nil, fmt.Errorf("database: mongodb.uri is required")
	}

	client, err := mongodb.New(buildOptions(sec)...)
	if err != nil {
		return nil, nil, fmt.Errorf("database: build mongodb client: %w", err)
	}
	return client, func() { client.Close() }, nil
}

// buildOptions 契约段字段 → bald-crud/mongodb options（纯函数，可测）。
// TLS（tls.Config）契约未建模，暂不支持——需要时先扩契约段。
func buildOptions(sec *bootstrapv1.Database_MongoDB) []mongodb.Option {
	var opts []mongocrud.Option
	opts = append(opts, mongocrud.WithURI(sec.GetUri()))
	if db := sec.GetDatabase(); db != "" {
		opts = append(opts, mongocrud.WithDatabase(db))
	}
	if u := sec.GetUsername(); u != "" {
		opts = append(opts, mongocrud.WithCredentials(u, sec.GetPassword()))
	}
	if s := sec.GetTimeoutSeconds(); s > 0 {
		opts = append(opts, mongocrud.WithTimeout(time.Duration(s)*time.Second))
	}
	if s := sec.GetConnectTimeoutSeconds(); s > 0 {
		opts = append(opts, mongocrud.WithConnectTimeout(time.Duration(s)*time.Second))
	}
	if s := sec.GetServerSelectionTimeoutSeconds(); s > 0 {
		opts = append(opts, mongocrud.WithServerSelectionTimeout(time.Duration(s)*time.Second))
	}
	if s := sec.GetHeartbeatIntervalSeconds(); s > 0 {
		opts = append(opts, mongocrud.WithHeartbeatInterval(time.Duration(s)*time.Second))
	}
	if s := sec.GetMaxConnIdleTimeSeconds(); s > 0 {
		opts = append(opts, mongocrud.WithMaxConnIdleTime(time.Duration(s)*time.Second))
	}
	return opts
}
