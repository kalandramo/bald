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

// FromBootstrap 契约装配：阶段 B 构建 → Broker 取用 → 停机 cleanup。
// Registry 行为测试（注册/段枚举/回滚）已随六域 Registry 迁 bootstrap
// （bootstrap/broker_test.go，2026-09-15）。

func TestFromBootstrap_ContractBrokerLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	type stubBroker struct{ name string }
	cleaned := false
	br := baldbootstrap.NewBrokerRegistry()
	br.MustRegister("redis", func(context.Context, *bootstrapv1.Broker) (any, func(), error) {
		return &stubBroker{name: "redis"}, func() { cleaned = true }, nil
	})

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Broker = &bootstrapv1.Broker{
		Redis: &bootstrapv1.Broker_Redis{Address: "127.0.0.1:6379"},
	}

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)), WithBrokerRegistry(br))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	if _, ok := a.Broker("redis"); ok {
		t.Fatal("Broker should be empty before Run (phase B not reached)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	b, ok := a.Broker("redis")
	if !ok {
		t.Fatal("broker instance not built from contract")
	}
	if c, ok := b.(*stubBroker); !ok || c.name != "redis" {
		t.Fatalf("instance type mismatch: %T", b)
	}
	if all := a.Brokers(); len(all) != 1 {
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

// 契约 broker 段存在但未接 BrokerRegistry → 阶段 B fail-fast。
func TestFromBootstrap_BrokerSectionWithoutTable(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Broker = &bootstrapv1.Broker{
		Redis: &bootstrapv1.Broker_Redis{Address: "127.0.0.1:6379"},
	}

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	err = a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "BrokerRegistry") {
		t.Fatalf("expected BrokerRegistry missing error, got %v", err)
	}
}
