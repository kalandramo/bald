package contract

import (
	"context"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/audit"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestNewStreamProvider_Build 契约：装配产物发布到契约声明的 stream，
// cleanup（Close）drain 尾批。
func TestNewStreamProvider_Build(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	p := NewStreamProvider(rdb)
	a, cleanup, err := p(context.Background(), &bootstrapv1.Audit{
		Backends: []string{TypeStream},
		Stream:   &bootstrapv1.Audit_Stream{Stream: "audit.custom", Buffer: 64},
	})
	if err != nil || a == nil || cleanup == nil {
		t.Fatalf("build: a=%v cleanup-set=%v err=%v", a, cleanup != nil, err)
	}

	a.Record(context.Background(), audit.AuditEvent{Subject: "u1", Object: "secret", Result: audit.ResultAllow})
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	n, err := rdb.XLen(context.Background(), "audit.custom").Result()
	if err != nil {
		t.Fatalf("xlen: %v", err)
	}
	if n != 1 {
		t.Fatalf("stream length = %d, want 1", n)
	}
}

// TestNewStreamProvider_Defaults 契约：stream/buffer 空值走实现缺省
// （audit.events / 1024），不报错。
func TestNewStreamProvider_Defaults(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	p := NewStreamProvider(rdb)
	a, cleanup, err := p(context.Background(), &bootstrapv1.Audit{Backends: []string{TypeStream}})
	if err != nil || a == nil || cleanup == nil {
		t.Fatalf("build: a=%v cleanup-set=%v err=%v", a, cleanup != nil, err)
	}

	a.Record(context.Background(), audit.AuditEvent{Subject: "u2"})
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	n, _ := rdb.XLen(context.Background(), "audit.events").Result()
	if n != 1 {
		t.Fatalf("default stream length = %d, want 1", n)
	}
}

// TestNewStreamProvider_NilClientFails 契约：nil 客户端 Build 时 fail-fast。
func TestNewStreamProvider_NilClientFails(t *testing.T) {
	p := NewStreamProvider(nil)
	if _, _, err := p(context.Background(), &bootstrapv1.Audit{Backends: []string{TypeStream}}); err == nil {
		t.Fatal("nil client should fail")
	}
}

// TestNewStreamProvider_BufferRespected 契约：buffer=1 时缓冲满降级不阻塞。
func TestNewStreamProvider_BufferRespected(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	p := NewStreamProvider(rdb)
	a, cleanup, err := p(context.Background(), &bootstrapv1.Audit{
		Backends: []string{TypeStream},
		Stream:   &bootstrapv1.Audit_Stream{Buffer: 1},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// 先 Close（后台退出、drain 空缓冲）→ 后续 Record 只入队不消费。
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.Record(context.Background(), audit.AuditEvent{Subject: "e1"}) // 入队
		a.Record(context.Background(), audit.AuditEvent{Subject: "e2"}) // 满→降级
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Record blocked on full buffer")
	}
}
