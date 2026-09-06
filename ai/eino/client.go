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

// newCloudChatModel 创建云端模型（基于 Eino OpenAI 实现）。
func newCloudChatModel(ctx context.Context, cfg *Config, o *options) (model.ChatModel, error) {
	if cfg.Cloud == nil {
		return nil, errors.New("cloud config is nil")
	}

	config := &einoOpenai.ChatModelConfig{
		APIKey: cfg.Cloud.ApiKey,
		Model:  cfg.ModelName,
	}

	if cfg.Cloud.BaseUrl != "" {
		config.BaseURL = cfg.Cloud.BaseUrl
	}
	if cfg.TimeoutSeconds > 0 {
		config.Timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}

	return einoOpenai.NewChatModel(ctx, applyConfigModifier(config, o))
}

// newOllamaChatModel 创建本地模型（基于 Eino OpenAI 实现，兼容 Ollama）。
func newOllamaChatModel(ctx context.Context, cfg *Config, o *options) (model.ChatModel, error) {
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

	config := &einoOpenai.ChatModelConfig{
		APIKey:  "ollama",
		BaseURL: fmt.Sprintf("http://%s:%d/v1", host, port),
		Model:   cfg.ModelName,
	}
	if cfg.TimeoutSeconds > 0 {
		config.Timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
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
