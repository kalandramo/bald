package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// --- AiRegistry：显式注册与 fail-fast（自 pkg/appkit 迁入 2026-09-15）---

func TestAiRegistry_FailFast(t *testing.T) {
	ar := NewAiRegistry()
	ar.MustRegister("openai", func(context.Context, *bootstrapv1.Ai) (any, func(), error) {
		return nil, nil, nil
	})

	// 重名
	err := ar.Register("openai", func(context.Context, *bootstrapv1.Ai) (any, func(), error) {
		return nil, nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	// 空类型 / nil provider
	if err := ar.Register("", nil); err == nil {
		t.Fatal("expected empty type error")
	}
	if err := ar.Register("eino", nil); err == nil {
		t.Fatal("expected nil provider error")
	}
	// Build：契约段 nil → no-op；零段非 nil 契约 → no-op
	clients, cleanup, err := ar.Build(context.Background(), nil)
	if err != nil || clients != nil || cleanup != nil {
		t.Fatalf("nil section should be no-op, got (clients=%v, cleanup=%t, err=%v)", clients, cleanup != nil, err)
	}
	clients, cleanup, err = ar.Build(context.Background(), &bootstrapv1.Ai{})
	if err != nil || clients == nil || len(clients) != 0 || cleanup != nil {
		t.Fatalf("empty section should be no-op, got (clients=%v, cleanup=%t, err=%v)", clients, cleanup != nil, err)
	}
	// Build：段存在但 provider 未注册（用未注册的 eino 段）
	_, _, err = ar.Build(context.Background(), &bootstrapv1.Ai{
		Eino: &bootstrapv1.Ai_Eino{ModelType: 2},
	})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected not-registered error, got %v", err)
	}
}

// Build：多段并存 + 逆序回放；第二段失败回滚第一段。
func TestAiRegistry_BuildMultiAndRollback(t *testing.T) {
	ar := NewAiRegistry()
	cleaned := []string{}
	ar.MustRegister("openai", func(context.Context, *bootstrapv1.Ai) (any, func(), error) {
		return "openai-client", func() { cleaned = append(cleaned, "openai") }, nil
	})
	ar.MustRegister("eino", func(context.Context, *bootstrapv1.Ai) (any, func(), error) {
		return nil, nil, errors.New("boom")
	})

	// openai 成功、eino 失败 → 回滚 openai
	_, _, err := ar.Build(context.Background(), &bootstrapv1.Ai{
		Openai: &bootstrapv1.Ai_Openai{ModelType: 2, Cloud: &bootstrapv1.Ai_CloudConfig{ApiKey: "k"}},
		Eino:   &bootstrapv1.Ai_Eino{ModelType: 2},
	})
	if err == nil || !strings.Contains(err.Error(), "build ai eino") {
		t.Fatalf("expected eino build error, got %v", err)
	}
	if len(cleaned) != 1 || cleaned[0] != "openai" {
		t.Fatalf("openai should be rolled back, cleaned=%v", cleaned)
	}

	// 三段成功：全部返回 + 逆序回放（eino→langchaingo→openai）
	ar2 := NewAiRegistry()
	ar2.MustRegister("openai", func(context.Context, *bootstrapv1.Ai) (any, func(), error) {
		return "openai-client", func() { cleaned = append(cleaned, "openai2") }, nil
	})
	ar2.MustRegister("langchaingo", func(context.Context, *bootstrapv1.Ai) (any, func(), error) {
		return "lc-model", func() { cleaned = append(cleaned, "langchaingo") }, nil
	})
	ar2.MustRegister("eino", func(context.Context, *bootstrapv1.Ai) (any, func(), error) {
		return "eino-model", func() { cleaned = append(cleaned, "eino") }, nil
	})
	clients, cleanup, err := ar2.Build(context.Background(), &bootstrapv1.Ai{
		Openai:      &bootstrapv1.Ai_Openai{ModelType: 2, Cloud: &bootstrapv1.Ai_CloudConfig{ApiKey: "k"}},
		Langchaingo: &bootstrapv1.Ai_Langchaingo{ModelType: 2, Cloud: &bootstrapv1.Ai_CloudConfig{ApiKey: "k"}},
		Eino:        &bootstrapv1.Ai_Eino{ModelType: 2, Cloud: &bootstrapv1.Ai_CloudConfig{ApiKey: "k"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if clients["openai"] != "openai-client" || clients["langchaingo"] != "lc-model" || clients["eino"] != "eino-model" {
		t.Fatalf("clients mismatch: %v", clients)
	}
	cleanup()
	if len(cleaned) != 4 || cleaned[1] != "eino" || cleaned[2] != "langchaingo" || cleaned[3] != "openai2" {
		t.Fatalf("cleanup order = %v, want [openai eino langchaingo openai2]", cleaned)
	}
}
