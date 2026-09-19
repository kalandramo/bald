package eino

import (
	"testing"
	"time"
)

// TestResolveTimeout 钉住三后端统一的超时默认语义：
// >0 用指定值，否则回退 30s 默认（与 ai/openai、ai/langchaingo 一致）。
//
// 缺陷背景：修复前 eino 仅在 TimeoutSeconds>0 时设 Timeout，未配置时
// config.Timeout=0 → eino-ext 构造 `&http.Client{Timeout: 0}`（永不超时），
// 服务端无响应即永久挂起。openai/langchaingo 同场景走 30s 默认。
func TestResolveTimeout(t *testing.T) {
	cases := []struct {
		name string
		sec  int32
		want time.Duration
	}{
		{"未配置回退 30s 默认", 0, 30 * time.Second},
		{"负值回退 30s 默认", -5, 30 * time.Second},
		{"正值用指定值", 60, 60 * time.Second},
		{"1 秒", 1, 1 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveTimeout(c.sec); got != c.want {
				t.Errorf("resolveTimeout(%d) = %v, want %v", c.sec, got, c.want)
			}
		})
	}
}

// TestCloudConfigCarriesTimeout 验证超时真正落到 eino-ext 的 ChatModelConfig，
// 而非只停留在 resolveTimeout 的返回值。
func TestCloudConfigCarriesTimeout(t *testing.T) {
	c, err := newCloudChatModelConfig(&Config{
		ModelName: "m",
		Cloud:     &CloudConfig{ApiKey: "k"},
	})
	if err != nil {
		t.Fatalf("newCloudChatModelConfig: %v", err)
	}
	if c.Timeout != 30*time.Second {
		t.Errorf("未配置超时：ChatModelConfig.Timeout = %v, want 30s（修复前为 0=永不超时）", c.Timeout)
	}

	c, err = newCloudChatModelConfig(&Config{
		ModelName:      "m",
		TimeoutSeconds: 120,
		Cloud:          &CloudConfig{ApiKey: "k"},
	})
	if err != nil {
		t.Fatalf("newCloudChatModelConfig: %v", err)
	}
	if c.Timeout != 120*time.Second {
		t.Errorf("配置 120s：ChatModelConfig.Timeout = %v, want 120s", c.Timeout)
	}
}

// TestLocalConfigCarriesTimeout 本地（Ollama）路径同样带默认超时。
func TestLocalConfigCarriesTimeout(t *testing.T) {
	c, err := newLocalChatModelConfig(&Config{
		ModelName: "m",
		Local:     &LocalConfig{Host: "h"},
	})
	if err != nil {
		t.Fatalf("newLocalChatModelConfig: %v", err)
	}
	if c.Timeout != 30*time.Second {
		t.Errorf("本地未配置超时：Timeout = %v, want 30s", c.Timeout)
	}
	if c.BaseURL != "http://h:11434/v1" {
		t.Errorf("本地 BaseURL = %q, want http://h:11434/v1", c.BaseURL)
	}
}
