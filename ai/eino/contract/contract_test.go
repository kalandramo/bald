package contract

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	eino "github.com/kalandramo/bald/ai/eino"
)

func TestType(t *testing.T) {
	if Type != "eino" {
		t.Errorf("Type = %q, want %q", Type, "eino")
	}
}

func TestProvider_MissingSection(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Ai{})
	if err == nil {
		t.Fatal("Provider with missing eino section should fail")
	}
	want := `ai: type="eino" but eino section is missing`
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
	cc, err := buildConfig(2, "doubao-pro", 30,
		&bootstrapv1.Ai_CloudConfig{ApiKey: "sk", BaseUrl: "https://ark.cn-x.volces.com/api/v3"}, nil)
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cc.Type != eino.ModelTypeCloud || cc.ModelName != "doubao-pro" || cc.TimeoutSeconds != 30 {
		t.Errorf("mapping mismatch: %+v", cc)
	}
	if cc.Cloud == nil || cc.Cloud.ApiKey != "sk" || cc.Cloud.BaseUrl != "https://ark.cn-x.volces.com/api/v3" {
		t.Errorf("cloud mismatch: %+v", cc.Cloud)
	}

	cc, err = buildConfig(1, "qwen2.5", 0, nil, &bootstrapv1.Ai_LocalConfig{Host: "127.0.0.1", Port: 11434})
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cc.Type != eino.ModelTypeLocal || cc.Local == nil || cc.Local.Port != 11434 {
		t.Errorf("local mapping mismatch: %+v", cc)
	}
}
