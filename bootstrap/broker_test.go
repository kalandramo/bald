package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// --- BrokerRegistry：显式注册与 fail-fast（自 pkg/appkit 迁入 2026-09-15）---

func TestBrokerRegistry_FailFast(t *testing.T) {
	br := NewBrokerRegistry()
	br.MustRegister("kafka", func(context.Context, *bootstrapv1.Broker) (any, func(), error) {
		return nil, nil, nil
	})

	if err := br.Register("kafka", func(context.Context, *bootstrapv1.Broker) (any, func(), error) {
		return nil, nil, nil
	}); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	if err := br.Register("", nil); err == nil {
		t.Fatal("expected empty type error")
	}
	if err := br.Register("redis", nil); err == nil {
		t.Fatal("expected nil provider error")
	}
	// Build：契约段 nil → no-op
	clients, cleanup, err := br.Build(context.Background(), nil)
	if err != nil || clients != nil || cleanup != nil {
		t.Fatalf("nil section should be no-op, got (clients=%v, cleanup=%t, err=%v)", clients, cleanup != nil, err)
	}
	// Build：段存在但 provider 未注册（用未注册的 redis 段）
	_, _, err = br.Build(context.Background(), &bootstrapv1.Broker{
		Redis: &bootstrapv1.Broker_Redis{Address: "127.0.0.1:6379"},
	})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected not-registered error, got %v", err)
	}
}

// Build：多段并存 + 逆序回放；第二段失败回滚第一段。
func TestBrokerRegistry_BuildMultiAndRollback(t *testing.T) {
	cleaned := []string{}
	br := NewBrokerRegistry()
	br.MustRegister("kafka", func(context.Context, *bootstrapv1.Broker) (any, func(), error) {
		return "kafka-broker", func() { cleaned = append(cleaned, "kafka") }, nil
	})
	br.MustRegister("redis", func(context.Context, *bootstrapv1.Broker) (any, func(), error) {
		return nil, nil, errors.New("boom")
	})

	// kafka 成功、redis 失败 → 回滚 kafka
	_, _, err := br.Build(context.Background(), &bootstrapv1.Broker{
		Kafka: &bootstrapv1.Broker_Kafka{Brokers: []string{"127.0.0.1:9092"}},
		Redis: &bootstrapv1.Broker_Redis{Address: "127.0.0.1:6379"},
	})
	if err == nil || !strings.Contains(err.Error(), "build broker redis") {
		t.Fatalf("expected redis build error, got %v", err)
	}
	if len(cleaned) != 1 || cleaned[0] != "kafka" {
		t.Fatalf("kafka should be rolled back, cleaned=%v", cleaned)
	}

	// 两段成功：全部返回 + 逆序回放（redis→kafka）
	br2 := NewBrokerRegistry()
	br2.MustRegister("kafka", func(context.Context, *bootstrapv1.Broker) (any, func(), error) {
		return "kafka-broker", func() { cleaned = append(cleaned, "kafka2") }, nil
	})
	br2.MustRegister("redis", func(context.Context, *bootstrapv1.Broker) (any, func(), error) {
		return "redis-broker", func() { cleaned = append(cleaned, "redis") }, nil
	})
	clients, cleanup, err := br2.Build(context.Background(), &bootstrapv1.Broker{
		Kafka: &bootstrapv1.Broker_Kafka{Brokers: []string{"127.0.0.1:9092"}},
		Redis: &bootstrapv1.Broker_Redis{Address: "127.0.0.1:6379"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if clients["kafka"] != "kafka-broker" || clients["redis"] != "redis-broker" {
		t.Fatalf("clients mismatch: %v", clients)
	}
	cleanup()
	if len(cleaned) != 3 || cleaned[1] != "redis" || cleaned[2] != "kafka2" {
		t.Fatalf("cleanup order = %v, want [kafka redis kafka2]", cleaned)
	}
}
