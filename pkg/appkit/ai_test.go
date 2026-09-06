package appkit

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
)

// --- AiRegistry：显式注册与 fail-fast ---

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

// --- FromBootstrap 契约装配：阶段 B 构建 → Ai 取用 → 停机 cleanup ---

func TestFromBootstrap_ContractAiLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	type stubClient struct{ name string }
	cleaned := false
	ar := NewAiRegistry()
	ar.MustRegister("openai", func(context.Context, *bootstrapv1.Ai) (any, func(), error) {
		return &stubClient{name: "openai"}, func() { cleaned = true }, nil
	})

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Ai = &bootstrapv1.Ai{
		Openai: &bootstrapv1.Ai_Openai{ModelType: 2, Cloud: &bootstrapv1.Ai_CloudConfig{ApiKey: "sk"}},
	}

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)), WithAiRegistry(ar))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	if _, ok := a.Ai("openai"); ok {
		t.Fatal("Ai should be empty before Run (phase B not reached)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	cli, ok := a.Ai("openai")
	if !ok {
		t.Fatal("ai instance not built from contract")
	}
	if c, ok := cli.(*stubClient); !ok || c.name != "openai" {
		t.Fatalf("instance type mismatch: %T", cli)
	}
	if all := a.Ais(); len(all) != 1 {
		t.Fatalf("Ais() = %v, want 1 entry", all)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s")
	}
	if !cleaned {
		t.Fatal("ai cleanup should run on shutdown")
	}
}

// 契约 ai 段存在但未接 AiRegistry → 阶段 B fail-fast。
func TestFromBootstrap_AiSectionWithoutTable(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Ai = &bootstrapv1.Ai{
		Openai: &bootstrapv1.Ai_Openai{ModelType: 2, Cloud: &bootstrapv1.Ai_CloudConfig{ApiKey: "sk"}},
	}

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	err = a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "AiRegistry") {
		t.Fatalf("expected AiRegistry missing error, got %v", err)
	}
}
