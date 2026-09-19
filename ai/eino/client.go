package eino

import (
	"context"
	"errors"
	"fmt"
	"time"

	einoOpenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

// NewChatModel 根据配置创建 Eino ChatModel。
// 支持云端模型（OpenAI 兼容 API）和本地模型（Ollama）。
func NewChatModel(ctx context.Context, cfg *Config, opts ...Option) (model.ChatModel, error) {
	if cfg == nil {
		return nil, errors.New("ai model config is nil")
	}

	o := applyOptions(opts)

	switch cfg.Type {
	case ModelTypeLocal:
		return newOllamaChatModel(ctx, cfg, o)
	case ModelTypeCloud:
		return newCloudChatModel(ctx, cfg, o)
	default:
		return nil, fmt.Errorf("unsupported ai model type: %v", cfg.Type)
	}
}

// defaultTimeout 是未显式配置 timeout_seconds 时的回退超时。
// 与 ai/openai、ai/langchaingo 保持一致（30s）——三后端同形契约下
// 同场景不应有不同超时语义。零值 Timeout 会让 eino-ext 构造
// `&http.Client{Timeout: 0}`（永不超时），服务端无响应即永久挂起。
const defaultTimeout = 30 * time.Second

// resolveTimeout 把契约的 timeout_seconds 归一为 time.Duration：
// 正值用指定值，<=0 回退 defaultTimeout（纯函数，可测）。
func resolveTimeout(sec int32) time.Duration {
	if sec > 0 {
		return time.Duration(sec) * time.Second
	}
	return defaultTimeout
}

// newCloudChatModelConfig 构造云端 ChatModelConfig（纯函数，可测）。
func newCloudChatModelConfig(cfg *Config) (*einoOpenai.ChatModelConfig, error) {
	if cfg.Cloud == nil {
		return nil, errors.New("cloud config is nil")
	}
	config := &einoOpenai.ChatModelConfig{
		APIKey:  cfg.Cloud.ApiKey,
		Model:   cfg.ModelName,
		Timeout: resolveTimeout(cfg.TimeoutSeconds),
	}
	if cfg.Cloud.BaseUrl != "" {
		config.BaseURL = cfg.Cloud.BaseUrl
	}
	return config, nil
}

// newLocalChatModelConfig 构造本地（Ollama）ChatModelConfig（纯函数，可测）。
func newLocalChatModelConfig(cfg *Config) (*einoOpenai.ChatModelConfig, error) {
	if cfg.Local == nil {
		return nil, errors.New("local config is nil")
	}
	host := cfg.Local.Host
	if host == "" {
		host = "localhost"
	}
	port := cfg.Local.Port
	if port == 0 {
		port = 11434
	}
	return &einoOpenai.ChatModelConfig{
		APIKey:  "ollama",
		BaseURL: fmt.Sprintf("http://%s:%d/v1", host, port),
		Model:   cfg.ModelName,
		Timeout: resolveTimeout(cfg.TimeoutSeconds),
	}, nil
}

// newCloudChatModel 创建云端模型（基于 Eino OpenAI 实现）。
func newCloudChatModel(ctx context.Context, cfg *Config, o *options) (model.ChatModel, error) {
	config, err := newCloudChatModelConfig(cfg)
	if err != nil {
		return nil, err
	}
	return einoOpenai.NewChatModel(ctx, applyConfigModifier(config, o))
}

// newOllamaChatModel 创建本地模型（基于 Eino OpenAI 实现，兼容 Ollama）。
func newOllamaChatModel(ctx context.Context, cfg *Config, o *options) (model.ChatModel, error) {
	config, err := newLocalChatModelConfig(cfg)
	if err != nil {
		return nil, err
	}
	return einoOpenai.NewChatModel(ctx, applyConfigModifier(config, o))
}

// applyConfigModifier 应用配置修饰器。
func applyConfigModifier(config *einoOpenai.ChatModelConfig, o *options) *einoOpenai.ChatModelConfig {
	if o.configModifier != nil {
		o.configModifier(config)
	}
	return config
}
