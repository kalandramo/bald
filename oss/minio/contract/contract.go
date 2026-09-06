// Package contract 提供 minio 后端的契约装配：bootstrapv1.Storage 的 minio
// 段 → bald/oss/minio Config 映射 + StorageRegistry Provider。
//
// 单独成包的原因：minio 包保持零契约依赖（纯 SDK 封装），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	minio "github.com/kalandramo/bald/oss/minio"
)

// Type 是契约 storage 段中 minio 后端的段名。
const Type = "minio"

// Provider 按契约 storage.minio 段构造 MinIO 客户端与 Storage 门面。
// 返回的 cleanup 为 nil（minio.Client 无连接池资源，HTTP 传输由 SDK 自管）。
// 仅当 storage.minio 段存在时被 StorageRegistry 调度到。
// 签名与 appkit.StorageProvider 结构化兼容，直接注册：
//
//	sr := appkit.NewStorageRegistry()
//	sr.MustRegister(miniocontract.Type, miniocontract.Provider)
func Provider(ctx context.Context, cfg *bootstrapv1.Storage) (any, func(), error) {
	sec := cfg.GetMinio()
	if sec == nil {
		return nil, nil, fmt.Errorf("storage: type=%q but minio section is missing", Type)
	}
	if sec.GetEndpoint() == "" {
		return nil, nil, fmt.Errorf("storage: minio.endpoint is required")
	}

	client := minio.NewStorage(&minio.Config{
		Endpoint:  sec.GetEndpoint(),
		AccessKey: sec.GetAccessKey(),
		SecretKey: sec.GetSecretKey(),
		Token:     sec.GetToken(),
		UseSsl:    sec.GetUseSsl(),
	})
	return client, nil, nil
}

// 类型守卫：Provider 签名与 appkit.StorageProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Storage) (any, func(), error) = Provider
