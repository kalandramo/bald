// Package contract 提供 s3 后端的契约装配：bootstrapv1.Storage 的 s3 段 →
// bald/oss/s3 Config 映射 + StorageRegistry Provider。
//
// 单独成包的原因：s3 包保持零契约依赖（纯 SDK 封装），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	s3 "github.com/kalandramo/bald/oss/s3"
)

// Type 是契约 storage 段中 s3 后端的段名。
const Type = "s3"

// Provider 按契约 storage.s3 段构造 S3 兼容（AWS S3 / Ceph / MinIO 等）
// 客户端与 Storage 门面。
// 返回的 cleanup 为 nil（aws-sdk 客户端无显式 Close 语义）。
// 仅当 storage.s3 段存在时被 StorageRegistry 调度到。
// 签名与 bootstrap.StorageProvider 结构化兼容，直接注册：
//
//	sr := bootstrap.NewStorageRegistry()
//	sr.MustRegister(s3contract.Type, s3contract.Provider)
func Provider(ctx context.Context, cfg *bootstrapv1.Storage) (any, func(), error) {
	sec := cfg.GetS3()
	if sec == nil {
		return nil, nil, fmt.Errorf("storage: type=%q but s3 section is missing", Type)
	}
	if sec.GetRegion() == "" {
		return nil, nil, fmt.Errorf("storage: s3.region is required")
	}

	client := s3.NewStorage(&s3.Config{
		Endpoint:       sec.GetEndpoint(),
		Region:         sec.GetRegion(),
		AccessKey:      sec.GetAccessKey(),
		SecretKey:      sec.GetSecretKey(),
		Token:          sec.GetToken(),
		UseSsl:         sec.GetUseSsl(),
		ForcePathStyle: sec.GetForcePathStyle(),
		Bucket:         sec.GetBucket(),
	})
	// fail-closed：NewStorage 即使底层 client 构造失败（NewClient 出错时 log 后
	// 返回 nil）也会返回非 nil 门面。装配期在此拦下，避免把「配置错误」推迟到
	// 首次 PutObject 才以 nil 暴露。
	if client == nil || client.SDK() == nil {
		return nil, nil, fmt.Errorf("storage: s3 client construction failed for region %q", sec.GetRegion())
	}
	return client, nil, nil
}

// 类型守卫：Provider 签名与 bootstrap.StorageProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Storage) (any, func(), error) = Provider
