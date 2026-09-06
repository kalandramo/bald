# openai 包说明

## 概述

`openai` 包基于 [go-openai](https://github.com/sashabaranov/go-openai) 封装，提供 OpenAI 兼容的 LLM 客户端创建能力。

支持两种部署模式：

- **云端模型**（`ModelTypeCloud`）：OpenAI、通义千问等兼容 OpenAI API 的云端服务
- **本地模型**（`ModelTypeLocal`）：通过 Ollama 等工具部署的本地模型

## 特性

- 通过本地配置结构体 `Config` 统一初始化，无需硬编码连接参数
- 支持自定义 Base URL，兼容各种 OpenAI 兼容 API
- 支持自定义 HTTP 客户端和超时设置
- 支持多组织（Organization）配置

## API 概览

| 函数 | 说明 |
|------|------|
| `NewClient(cfg *Config, opts ...Option) (*openai.Client, error)` | 根据配置创建客户端 |
| `WithHTTPClient(httpClient *http.Client) Option` | 设置自定义 HTTP 客户端 |

返回的 `*openai.Client` 可直接使用 go-openai 的全部 API，包括 Chat Completion、Embedding、Function Calling 等。

## 使用示例

### 云端模型

```go
package example

import (
    openai "github.com/sashabaranov/go-openai"
    aiOpenai "github.com/kalandramo/bald/ai/openai"
)

func NewCloudClient() (*openai.Client, error) {
    cfg := &aiOpenai.Config{
        Type:      aiOpenai.ModelTypeCloud,
        ModelName: "gpt-4o",
        Cloud: &aiOpenai.CloudConfig{
            ApiKey:    "sk-xxx",
            BaseUrl:   "https://api.openai.com/v1",
        },
        TimeoutSeconds: 60,
    }
    return aiOpenai.NewClient(cfg)
}
```

### 本地模型（Ollama）

```go
func NewLocalClient() (*openai.Client, error) {
    cfg := &aiOpenai.Config{
        Type:      aiOpenai.ModelTypeLocal,
        ModelName: "llama3",
        Local: &aiOpenai.LocalConfig{
            Host: "localhost",
            Port: 11434,
        },
        TimeoutSeconds: 120,
    }
    return aiOpenai.NewClient(cfg)
}
```

### 调用 Chat Completion

```go
client, _ := aiOpenai.NewClient(cfg)

resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
    Model: cfg.ModelName,
    Messages: []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleSystem, Content: "你是一个翻译助手。"},
        {Role: openai.ChatMessageRoleUser, Content: "将以下内容翻译为英文：你好世界"},
    },
})
```
