package appkit

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
)

// 约定装配：阶段 B 构建 → Ai 取用 → 停机 cleanup。

func TestAssemble_ContractAiLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	type stubClient struct{ name string }
	cleaned := false
	prov := stubDomainProvider("ai", "openai",
		func(c *bootstrapv1.BootstrapConfig) bool { return c.GetAi().GetOpenai() != nil },
		&stubClient{name: "openai"}, func() { cleaned = true })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Ai = &bootstrapv1.Ai{
		Openai: &bootstrapv1.Ai_Openai{ModelType: 2, Cloud: &bootstrapv1.Ai_CloudConfig{ApiKey: "sk"}},
	}

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)), prov)
	if _, ok := a.lookup("ai.openai"); ok {
		t.Fatal("Ai should be empty before Run (phase B not reached)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	cli, ok := a.lookup("ai.openai")
	if !ok {
		t.Fatal("ai instance not built from contract")
	}
	if c, ok := cli.(*stubClient); !ok || c.name != "openai" {
		t.Fatalf("instance type mismatch: %T", cli)
	}
	if all := resourceMap(t, a, "ai"); len(all) != 1 {
		t.Fatalf("Resource(\"ai\") snapshot = %v, want 1 entry", all)
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

// 契约 ai 段存在但无 ai Provider → 阶段 B fail-fast。
func TestAssemble_AiSectionWithoutTable(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Ai = &bootstrapv1.Ai{
		Openai: &bootstrapv1.Ai_Openai{ModelType: 2, Cloud: &bootstrapv1.Ai_CloudConfig{ApiKey: "sk"}},
	}

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)))
	err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no consumer") || !strings.Contains(err.Error(), "ai") {
		t.Fatalf("expected no-consumer error for ai, got %v", err)
	}
}
