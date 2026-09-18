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

// Build：契约里声明了但本仓无实现的段（13 段超集里的 9 段）必须 fail-fast，
// 不得静默跳过——否则用户以为消息代理接上了、实际什么都没发生。
func TestBrokerRegistry_UnimplementedSectionFailsFast(t *testing.T) {
	cases := []struct {
		name string
		cfg  *bootstrapv1.Broker
	}{
		{"nats", &bootstrapv1.Broker{Nats: &bootstrapv1.Broker_Nats{Url: "nats://127.0.0.1:4222"}}},
		{"mqtt", &bootstrapv1.Broker{Mqtt: &bootstrapv1.Broker_Mqtt{Address: "tcp://127.0.0.1:1883"}}},
		{"pulsar", &bootstrapv1.Broker{Pulsar: &bootstrapv1.Broker_Pulsar{Url: "pulsar://localhost:6650"}}},
		{"azuresb", &bootstrapv1.Broker{Azuresb: &bootstrapv1.Broker_Azuresb{ConnectionString: "x"}}},
		{"gcpubsub", &bootstrapv1.Broker{Gcpubsub: &bootstrapv1.Broker_Gcpubsub{ProjectId: "p"}}},
		{"nsq", &bootstrapv1.Broker{Nsq: &bootstrapv1.Broker_Nsq{Addrs: []string{"127.0.0.1:4150"}}}},
		{"sqs", &bootstrapv1.Broker{Sqs: &bootstrapv1.Broker_Sqs{Region: "us-east-1"}}},
		{"stomp", &bootstrapv1.Broker{Stomp: &bootstrapv1.Broker_Stomp{Address: "stomp://127.0.0.1:61613"}}},
		{"activemq", &bootstrapv1.Broker{Activemq: &bootstrapv1.Broker_Activemq{Address: "stomp://127.0.0.1:61613"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := NewBrokerRegistry()
			_, _, err := br.Build(context.Background(), c.cfg)
			if err == nil {
				t.Fatalf("broker.%s is unimplemented; Build must fail fast, got nil error", c.name)
			}
			if !strings.Contains(err.Error(), "not implemented") {
				t.Fatalf("error should say not implemented, got: %v", err)
			}
		})
	}
}

// 未实现段与已实现段并存时，回滚已实现的段（不泄漏连接）。
func TestBrokerRegistry_UnimplementedSectionRollsBack(t *testing.T) {
	cleaned := 0
	br := NewBrokerRegistry()
	br.MustRegister("kafka", func(context.Context, *bootstrapv1.Broker) (any, func(), error) {
		return "kafka-broker", func() { cleaned++ }, nil
	})
	_, _, err := br.Build(context.Background(), &bootstrapv1.Broker{
		Kafka: &bootstrapv1.Broker_Kafka{Brokers: []string{"127.0.0.1:9092"}},
		Nats:  &bootstrapv1.Broker_Nats{Url: "nats://127.0.0.1:4222"},
	})
	if err == nil {
		t.Fatal("expected fail-fast on unimplemented nats section")
	}
	if cleaned != 1 {
		t.Fatalf("kafka should be rolled back exactly once, cleaned=%d", cleaned)
	}
}
