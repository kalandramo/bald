package openai_test

import (
	"net/http"
	"testing"

	"github.com/kalandramo/bald/ai/openai"
)

// TestNewClientNilConfig 覆盖 cfg==nil 的 fail-fast 分支。
func TestNewClientNilConfig(t *testing.T) {
	if _, err := openai.NewClient(nil); err == nil {
		t.Fatal("期望 nil 配置返回错误")
	}
}

// TestNewClientUnsupportedType 覆盖 switch default 分支（既非 local 也非 cloud）。
func TestNewClientUnsupportedType(t *testing.T) {
	cfg := &openai.Config{Type: openai.ModelType(99)}
	if _, err := openai.NewClient(cfg); err == nil {
		t.Fatal("期望非法模型类型返回错误")
	}
}

// TestNewClientCloud 覆盖云端分支：Cloud 为 nil 报错 + 完整配置成功（含 BaseUrl/Org 两个可选 setter）。
func TestNewClientCloud(t *testing.T) {
	// Cloud==nil → 报错
	if _, err := openai.NewClient(&openai.Config{Type: openai.ModelTypeCloud}); err == nil {
		t.Fatal("期望 cloud 配置为 nil 报错")
	}
	// 完整配置 → 成功，走 BaseUrl/Organization 两分支
	cfg := &openai.Config{
		Type:           openai.ModelTypeCloud,
		TimeoutSeconds: 5,
		Cloud: &openai.CloudConfig{
			ApiKey:       "sk-test",
			BaseUrl:      "https://example.com/v1",
			Organization: "org-test",
		},
	}
	c, err := openai.NewClient(cfg)
	if err != nil {
		t.Fatalf("云端客户端构造失败: %v", err)
	}
	if c == nil {
		t.Fatal("云端客户端为 nil")
	}
}

// TestNewClientLocal 覆盖本地分支：Local 为 nil 报错 + 默认 host/port + 自定义 host/port。
func TestNewClientLocal(t *testing.T) {
	// Local==nil → 报错
	if _, err := openai.NewClient(&openai.Config{Type: openai.ModelTypeLocal}); err == nil {
		t.Fatal("期望 local 配置为 nil 报错")
	}
	// 空 host/port → 走 localhost/11434 默认
	if _, err := openai.NewClient(&openai.Config{
		Type:  openai.ModelTypeLocal,
		Local: &openai.LocalConfig{},
	}); err != nil {
		t.Fatalf("本地默认端口客户端构造失败: %v", err)
	}
	// 自定义 host/port + 自定义 HTTPClient（覆盖 setHTTPClient 的 o.httpClient!=nil 分支）
	if _, err := openai.NewClient(&openai.Config{
		Type:  openai.ModelTypeLocal,
		Local: &openai.LocalConfig{Host: "10.0.0.1", Port: 8080},
	}, openai.WithHTTPClient(&http.Client{})); err != nil {
		t.Fatalf("本地自定义客户端构造失败: %v", err)
	}
}
