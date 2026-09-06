package contract

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	langchaingo "github.com/kalandramo/bald/ai/langchaingo"
)

func TestType(t *testing.T) {
	if Type != "langchaingo" {
		t.Errorf("Type = %q, want %q", Type, "langchaingo")
	}
}

func TestProvider_MissingSection(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Ai{})
	if err == nil {
		t.Fatal("Provider with missing langchaingo section should fail")
	}
	want := `ai: type="langchaingo" but langchaingo section is missing`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// model_type 非法 / cloud 缺 api_key / local 缺 host fail-fast。
func TestBuildConfig_FailFast(t *testing.T) {
	if _, err := buildConfig(9, "m", 0, nil, nil); err == nil || !strings.Contains(err.Error(), "model_type=9") {
		t.Fatalf("expected invalid model_type error, got %v", err)
	}
	if _, err := buildConfig(2, "m", 0, nil, nil); err == nil || !strings.Contains(err.Error(), "cloud.api_key") {
		t.Fatalf("expected cloud.api_key error, got %v", err)
	}
	if _, err := buildConfig(1, "m", 0, nil, nil); err == nil || !strings.Contains(err.Error(), "local.host") {
		t.Fatalf("expected local.host error, got %v", err)
	}
}

// buildConfig 纯函数映射断言。
func TestBuildConfig_Mapping(t *testing.T) {
	cc, err := buildConfig(2, "qwen-max", 30,
		&bootstrapv1.Ai_CloudConfig{ApiKey: "sk", BaseUrl: "https://x/v1", Organization: "org"}, nil)
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cc.Type != langchaingo.ModelTypeCloud || cc.ModelName != "qwen-max" || cc.TimeoutSeconds != 30 {
		t.Errorf("mapping mismatch: %+v", cc)
	}
	if cc.Cloud == nil || cc.Cloud.ApiKey != "sk" || cc.Cloud.BaseUrl != "https://x/v1" || cc.Cloud.Organization != "org" {
		t.Errorf("cloud mismatch: %+v", cc.Cloud)
	}

	cc, err = buildConfig(1, "llama3", 0, nil, &bootstrapv1.Ai_LocalConfig{Host: "127.0.0.1", Port: 11434})
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cc.Type != langchaingo.ModelTypeLocal || cc.Local == nil || cc.Local.Host != "127.0.0.1" || cc.Local.Port != 11434 {
		t.Errorf("local mapping mismatch: %+v", cc)
	}
}

// Provider 端到端（构造不触网）：产出 llms.Model + cleanup 为 nil。
func TestProvider_CloudBuild(t *testing.T) {
	cli, cleanup, err := Provider(context.Background(), &bootstrapv1.Ai{
		Langchaingo: &bootstrapv1.Ai_Langchaingo{
			ModelType: 2,
			Cloud:     &bootstrapv1.Ai_CloudConfig{ApiKey: "sk-test"},
		},
	})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Error("langchaingo model has no pool to close, cleanup should be nil")
	}
	if _, ok := cli.(interface{ Call(ctx context.Context, prompt string, opts ...any) (string, error) }); !ok && cli == nil {
		t.Fatalf("instance should be non-nil llms.Model, got %T", cli)
	}
}
