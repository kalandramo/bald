package contract

import (
	"context"
	"strings"
	"testing"

	goopenai "github.com/sashabaranov/go-openai"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	openai "github.com/kalandramo/bald/ai/openai"
)

func TestType(t *testing.T) {
	if Type != "openai" {
		t.Errorf("Type = %q, want %q", Type, "openai")
	}
}

// 段缺失 fail-fast（不触网）。
func TestProvider_MissingSection(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Ai{})
	if err == nil {
		t.Fatal("Provider with missing openai section should fail")
	}
	want := `ai: type="openai" but openai section is missing`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// model_type 非法 fail-fast。
func TestProvider_InvalidModelType(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Ai{
		Openai: &bootstrapv1.Ai_Openai{ModelType: 9},
	})
	if err == nil || !strings.Contains(err.Error(), "model_type=9") {
		t.Fatalf("expected invalid model_type error, got %v", err)
	}
}

// cloud 型缺 api_key fail-fast。
func TestProvider_CloudWithoutApiKey(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Ai{
		Openai: &bootstrapv1.Ai_Openai{ModelType: 2},
	})
	if err == nil || !strings.Contains(err.Error(), "cloud.api_key") {
		t.Fatalf("expected cloud.api_key error, got %v", err)
	}
}

// local 型缺 host fail-fast。
func TestProvider_LocalWithoutHost(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Ai{
		Openai: &bootstrapv1.Ai_Openai{ModelType: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "local.host") {
		t.Fatalf("expected local.host error, got %v", err)
	}
}

// buildConfig 纯函数映射断言（SDK Client.config 未导出，语义钉在这里）。

func TestBuildConfig_Cloud(t *testing.T) {
	cc, err := buildConfig(&bootstrapv1.Ai_Openai{
		ModelType:      2,
		ModelName:      "gpt-4o",
		TimeoutSeconds: 30,
		Cloud: &bootstrapv1.Ai_CloudConfig{
			ApiKey:       "sk-test",
			BaseUrl:      "https://api.example.com/v1",
			Organization: "org",
		},
	})
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cc.Type != openai.ModelTypeCloud {
		t.Errorf("Type = %v, want cloud", cc.Type)
	}
	if cc.ModelName != "gpt-4o" || cc.TimeoutSeconds != 30 {
		t.Errorf("model/timeout mismatch: %+v", cc)
	}
	if cc.Cloud == nil || cc.Cloud.ApiKey != "sk-test" || cc.Cloud.BaseUrl != "https://api.example.com/v1" || cc.Cloud.Organization != "org" {
		t.Errorf("cloud mismatch: %+v", cc.Cloud)
	}
	if cc.Local != nil {
		t.Error("cloud config should not carry local")
	}
}

func TestBuildConfig_Local(t *testing.T) {
	cc, err := buildConfig(&bootstrapv1.Ai_Openai{
		ModelType: 1,
		ModelName: "llama3",
		Local:     &bootstrapv1.Ai_LocalConfig{Host: "127.0.0.1", Port: 11434},
	})
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cc.Type != openai.ModelTypeLocal {
		t.Errorf("Type = %v, want local", cc.Type)
	}
	if cc.Local == nil || cc.Local.Host != "127.0.0.1" || cc.Local.Port != 11434 {
		t.Errorf("local mismatch: %+v", cc.Local)
	}
	if cc.Cloud != nil {
		t.Error("local config should not carry cloud")
	}
}

// Provider 端到端（构造不触网）：产出 SDK 客户端 + cleanup 为 nil。
func TestProvider_Build(t *testing.T) {
	cli, cleanup, err := Provider(context.Background(), &bootstrapv1.Ai{
		Openai: &bootstrapv1.Ai_Openai{
			ModelType: 2,
			Cloud:     &bootstrapv1.Ai_CloudConfig{ApiKey: "sk-test"},
		},
	})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Error("openai client has no pool to close, cleanup should be nil")
	}
	if _, ok := cli.(*goopenai.Client); !ok {
		t.Fatalf("instance type = %T, want *goopenai.Client", cli)
	}
}
