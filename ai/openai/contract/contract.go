// Package contract 提供 openai 后端的契约装配：bootstrapv1.Ai 的 openai 段 →
// bald/ai/openai Config 映射 + AiRegistry Provider。
//
// 单独成包的原因：openai 包保持零契约依赖（纯 SDK 封装），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	openai "github.com/kalandramo/bald/ai/openai"
)

// Type 是契约 ai 段中 openai 后端的段名。
const Type = "openai"

// Provider 按契约 ai.openai 段构造 OpenAI 兼容客户端。
// 返回的 cleanup 为 nil（客户端无连接池资源）。
// 仅当 ai.openai 段存在时被 AiRegistry 调度到。
// 签名与 appkit.AiProvider 结构化兼容，直接注册：
//
//	ar := appkit.NewAiRegistry()
//	ar.MustRegister(openaicontract.Type, openaicontract.Provider)
func Provider(ctx context.Context, cfg *bootstrapv1.Ai) (any, func(), error) {
	sec := cfg.GetOpenai()
	if sec == nil {
		return nil, nil, fmt.Errorf("ai: type=%q but openai section is missing", Type)
	}
	cc, err := buildConfig(sec)
	if err != nil {
		return nil, nil, err
	}
	client, err := openai.NewClient(cc)
	if err != nil {
		return nil, nil, fmt.Errorf("ai: build openai client: %w", err)
	}
	return client, nil, nil
}

// buildConfig 契约段字段 → openai Config（纯函数，可测；三后端段同构，
// 由 openai 版实现，eino/langchaingo 复用同形映射）。
// fail-fast：model_type 必须 1(local)/2(cloud)；cloud 型须带 cloud.api_key；
// local 型须带 local.host。
func buildConfig(sec *bootstrapv1.Ai_Openai) (*openai.Config, error) {
	cc := &openai.Config{
		ModelName:      sec.GetModelName(),
		TimeoutSeconds: sec.GetTimeoutSeconds(),
	}
	switch sec.GetModelType() {
	case 1:
		cc.Type = openai.ModelTypeLocal
		lc := sec.GetLocal()
		if lc == nil || lc.GetHost() == "" {
			return nil, fmt.Errorf("ai: openai model_type=local requires local.host")
		}
		cc.Local = &openai.LocalConfig{Host: lc.GetHost(), Port: lc.GetPort()}
	case 2:
		cc.Type = openai.ModelTypeCloud
		cl := sec.GetCloud()
		if cl == nil || cl.GetApiKey() == "" {
			return nil, fmt.Errorf("ai: openai model_type=cloud requires cloud.api_key")
		}
		cc.Cloud = &openai.CloudConfig{ApiKey: cl.GetApiKey(), BaseUrl: cl.GetBaseUrl(), Organization: cl.GetOrganization()}
	default:
		return nil, fmt.Errorf("ai: openai model_type=%d invalid (1=local, 2=cloud)", sec.GetModelType())
	}
	return cc, nil
}

// 类型守卫：Provider 签名与 appkit.AiProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Ai) (any, func(), error) = Provider
