package contract

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/broker"
)

func TestBuildDialURL(t *testing.T) {
	cases := []struct {
		name string
		addr string
		pass string
		db   int
		want string
	}{
		{"bare", "127.0.0.1:6379", "", 0, "redis://127.0.0.1:6379"},
		{"bare+pass", "127.0.0.1:6379", "pw", 0, "redis://:pw@127.0.0.1:6379"},
		{"bare+pass+db", "127.0.0.1:6379", "pw", 3, "redis://:pw@127.0.0.1:6379/3"},
		{"keep-scheme", "redis://127.0.0.1:6379", "", 0, "redis://127.0.0.1:6379"},
		{"keep-userinfo", "redis://u:p@127.0.0.1:6379", "other", 0, "redis://u:p@127.0.0.1:6379"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := buildDialURL(c.addr, c.pass, c.db); got != c.want {
				t.Fatalf("buildDialURL(%q,%q,%d) = %q, want %q", c.addr, c.pass, c.db, got, c.want)
			}
		})
	}
}

func TestProviderPubSubRoundtrip(t *testing.T) {
	mr := miniredis.RunT(t)

	out, cleanup, err := Provider(context.Background(), &bootstrapv1.Broker{
		Redis: &bootstrapv1.Broker_Redis{Address: mr.Addr()},
	})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	defer cleanup()

	b, ok := out.(broker.Broker)
	if !ok {
		t.Fatalf("client should implement broker.Broker, got %T", out)
	}

	type payload struct {
		Value string `json:"value"`
	}

	var wg sync.WaitGroup
	got := make(chan payload, 1)
	wg.Add(1)

	sub, err := b.Subscribe("topic.ci", func(ctx context.Context, pub broker.Event) error {
		p, ok := pub.Message().Body.(*payload)
		if !ok {
			t.Errorf("body type = %T, want *payload", pub.Message().Body)
			return nil
		}
		got <- *p
		return nil
	}, func() any { return new(payload) })
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe(true) }()

	// 等订阅生效
	time.Sleep(200 * time.Millisecond)

	if err := b.Publish(context.Background(), "topic.ci", &broker.Message{Body: payload{Value: "hello"}}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case p := <-got:
		if p.Value != "hello" {
			t.Fatalf("payload value = %q, want %q", p.Value, "hello")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestProviderSectionMissing(t *testing.T) {
	if _, _, err := Provider(context.Background(), &bootstrapv1.Broker{}); err == nil {
		t.Fatal("expected error when redis section is missing")
	}
}
