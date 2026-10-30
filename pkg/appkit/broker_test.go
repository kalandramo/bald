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

// 约定装配：阶段 B 构建 → Broker 取用 → 停机 cleanup。

func TestAssemble_ContractBrokerLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	type stubBroker struct{ name string }
	cleaned := false
	prov := stubDomainProvider("broker", "redis",
		func(c *bootstrapv1.BootstrapConfig) bool { return c.GetBroker().GetRedis() != nil },
		&stubBroker{name: "redis"}, func() { cleaned = true })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Broker = &bootstrapv1.Broker{
		Redis: &bootstrapv1.Broker_Redis{Address: "127.0.0.1:6379"},
	}

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)), prov)
	if _, ok := a.lookup("broker.redis"); ok {
		t.Fatal("Broker should be empty before Run (phase B not reached)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	b, ok := a.lookup("broker.redis")
	if !ok {
		t.Fatal("broker instance not built from contract")
	}
	if c, ok := b.(*stubBroker); !ok || c.name != "redis" {
		t.Fatalf("instance type mismatch: %T", b)
	}
	if all := resourceMap(t, a, "broker"); len(all) != 1 {
		t.Fatalf("Brokers() = %v, want 1 entry", all)
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
		t.Fatal("broker cleanup should run on shutdown")
	}
}

// 契约 broker 段存在但无 broker Provider → 阶段 B fail-fast。
func TestAssemble_BrokerSectionWithoutTable(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Broker = &bootstrapv1.Broker{
		Redis: &bootstrapv1.Broker_Redis{Address: "127.0.0.1:6379"},
	}

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)))
	err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no consumer") || !strings.Contains(err.Error(), "broker") {
		t.Fatalf("expected no-consumer error for broker, got %v", err)
	}
}
