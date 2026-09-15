// Package contract 提供 eino 后端的契约装配：bootstrapv1.Ai 的 eino 段 →
// bald/ai/eino Config 映射 + AiRegistry Provider。
//
// 单独成包的原因：eino 包保持零契约依赖（纯 SDK 封装），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	eino "github.com/kalandramo/bald/ai/eino"
)

// Type 是契约 ai 段中 eino 后端的段名。
const Type = "eino"

// Provider 按契约 ai.eino 段构造 Eino ChatModel（字节跳动 Eino 生态）。
// 返回的 cleanup 为 nil（模型对象无连接池资源）。
// 仅当 ai.eino 段存在时被 AiRegistry 调度到。
// 签名与 bootstrap.AiProvider 结构化兼容，直接注册：
//
//	ar := bootstrap.NewAiRegistry()
//	ar.MustRegister(einocontract.Type, einocontract.Provider)
func Provider(ctx context.Context, cfg *bootstrapv1.Ai) (any, func(), error) {
	sec := cfg.GetEino()
	if sec == nil {
		return nil, nil, fmt.Errorf("ai: type=%q but eino section is missing", Type)
	}
	cc, err := buildConfig(sec.GetModelType(), sec.GetModelName(), sec.GetTimeoutSeconds(), sec.GetCloud(), sec.GetLocal())
	if err != nil {
		return nil, nil, err
	}
	model, err := eino.NewChatModel(ctx, cc)
	if err != nil {
		return nil, nil, fmt.Errorf("ai: build eino chat model: %w", err)
	}
	return model, nil, nil
}

// buildConfig 契约段字段 → eino Config（纯函数，可测）。
// fail-fast：model_type 必须 1(local)/2(cloud)；cloud 型须带 cloud.api_key；
// local 型须带 local.host。
func buildConfig(modelType int32, modelName string, timeoutSec int32, cloud *bootstrapv1.Ai_CloudConfig, local *bootstrapv1.Ai_LocalConfig) (*eino.Config, error) {
	cc := &eino.Config{
		ModelName:      modelName,
		TimeoutSeconds: timeoutSec,
	}
	switch modelType {
	case 1:
		cc.Type = eino.ModelTypeLocal
		if local == nil || local.GetHost() == "" {
			return nil, fmt.Errorf("ai: eino model_type=local requires local.host")
		}
		cc.Local = &eino.LocalConfig{Host: local.GetHost(), Port: local.GetPort()}
	case 2:
		cc.Type = eino.ModelTypeCloud
		if cloud == nil || cloud.GetApiKey() == "" {
			return nil, fmt.Errorf("ai: eino model_type=cloud requires cloud.api_key")
		}
		cc.Cloud = &eino.CloudConfig{ApiKey: cloud.GetApiKey(), BaseUrl: cloud.GetBaseUrl()}
	default:
		return nil, fmt.Errorf("ai: eino model_type=%d invalid (1=local, 2=cloud)", modelType)
	}
	return cc, nil
}

// 类型守卫：Provider 签名与 bootstrap.AiProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Ai) (any, func(), error) = Provider
