package appkit

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
	"github.com/kalandramo/bald/log"
)

// FromBootstrap 契约装配：阶段 B 构建 → Ai 取用 → 停机 cleanup。
// Registry 行为测试（注册/段枚举/回滚）已随六域 Registry 迁 bootstrap
// （bootstrap/ai_test.go，2026-09-15）。

func TestFromBootstrap_ContractAiLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	type stubClient struct{ name string }
	cleaned := false
	ar := baldbootstrap.NewAiRegistry()
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
