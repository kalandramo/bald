// Package contract 提供 langchaingo 后端的契约装配：bootstrapv1.Ai 的
// langchaingo 段 → bald/ai/langchaingo Config 映射 + AiRegistry Provider。
//
// 单独成包的原因：langchaingo 包保持零契约依赖（纯 SDK 封装），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	langchaingo "github.com/kalandramo/bald/ai/langchaingo"
)

// Type 是契约 ai 段中 langchaingo 后端的段名。
const Type = "langchaingo"

// Provider 按契约 ai.langchaingo 段构造 LangChainGo 模型。
// 返回的 cleanup 为 nil（模型对象无连接池资源）。
// 仅当 ai.langchaingo 段存在时被 AiRegistry 调度到。
// 签名与 appkit.AiProvider 结构化兼容，直接注册：
//
//	ar := appkit.NewAiRegistry()
//	ar.MustRegister(langchaincontract.Type, langchaincontract.Provider)
func Provider(ctx context.Context, cfg *bootstrapv1.Ai) (any, func(), error) {
	sec := cfg.GetLangchaingo()
	if sec == nil {
		return nil, nil, fmt.Errorf("ai: type=%q but langchaingo section is missing", Type)
	}
	cc, err := buildConfig(sec.GetModelType(), sec.GetModelName(), sec.GetTimeoutSeconds(), sec.GetCloud(), sec.GetLocal())
	if err != nil {
		return nil, nil, err
	}
	model, err := langchaingo.NewModel(cc)
	if err != nil {
		return nil, nil, fmt.Errorf("ai: build langchaingo model: %w", err)
	}
	return model, nil, nil
}

// buildConfig 契约段字段 → langchaingo Config（纯函数，可测）。
// fail-fast：model_type 必须 1(local)/2(cloud)；cloud 型须带 cloud.api_key；
// local 型须带 local.host。
func buildConfig(modelType int32, modelName string, timeoutSec int32, cloud *bootstrapv1.Ai_CloudConfig, local *bootstrapv1.Ai_LocalConfig) (*langchaingo.Config, error) {
	cc := &langchaingo.Config{
		ModelName:      modelName,
		TimeoutSeconds: timeoutSec,
	}
	switch modelType {
	case 1:
		cc.Type = langchaingo.ModelTypeLocal
		if local == nil || local.GetHost() == "" {
			return nil, fmt.Errorf("ai: langchaingo model_type=local requires local.host")
		}
		cc.Local = &langchaingo.LocalConfig{Host: local.GetHost(), Port: local.GetPort()}
	case 2:
		cc.Type = langchaingo.ModelTypeCloud
		if cloud == nil || cloud.GetApiKey() == "" {
			return nil, fmt.Errorf("ai: langchaingo model_type=cloud requires cloud.api_key")
		}
		cc.Cloud = &langchaingo.CloudConfig{ApiKey: cloud.GetApiKey(), BaseUrl: cloud.GetBaseUrl(), Organization: cloud.GetOrganization()}
	default:
		return nil, fmt.Errorf("ai: langchaingo model_type=%d invalid (1=local, 2=cloud)", modelType)
	}
	return cc, nil
}

// 类型守卫：Provider 签名与 appkit.AiProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Ai) (any, func(), error) = Provider
