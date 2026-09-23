package stream

// stream_nilpool_test.go —— pool 为 nil 时的防护（缺陷族守护，stream driver）。
//
// 与 pubsub 侧同源：`b.pool = nil`（未 Connect / Connect 探活失败 / 已
// Disconnect）后 publish/Subscribe/ensureGroup 直接 `b.pool.Get()` →
// `(*Pool)(nil).Get()` **panic**。
//
// stream 的 Disconnect 一直有 nil 防护（与 pubsub 不同），但 publish/
// Subscribe 两处同样遗漏。

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/broker"
)

// newNilPoolBroker 返回一个 pool 为 nil 的 stream broker。
func newNilPoolBroker(t *testing.T) broker.Broker {
	t.Helper()
	b := NewBroker(broker.WithAddress("redis://127.0.0.1:6399"))
	if err := b.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := b.Connect(); err == nil {
		t.Fatal("Connect 对不可达 Redis 应返回 error")
	}
	// 前置条件：pool 已因探活失败被丢弃。
	if sb, ok := b.(*streamBroker); ok && sb.pool != nil {
		t.Fatal("Connect 探活失败后 pool 应为 nil（前置条件不成立）")
	}
	return b
}

// TestPublish_NilPool_ReturnsErrorNotPanic —— publish 在 nil pool 下不得 panic。
func TestPublish_NilPool_ReturnsErrorNotPanic(t *testing.T) {
	b := newNilPoolBroker(t)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil pool 下 Publish panic（缺防护）: %v", r)
		}
	}()
	err := b.Publish(context.Background(), "s.nilpool",
		&broker.Message{Body: []byte("x")})
	if err == nil {
		t.Fatal("nil pool 下 Publish 应返回 error")
	}
	t.Logf("Publish 正确报错: %v", err)
}

// TestSubscribe_NilPool_ReturnsErrorNotPanic —— Subscribe 在 nil pool 下不得 panic。
//
// 注意：stream 的 Subscribe 内部会先调 ensureGroup（也用裸 pool.Get()），
// 故本测试同时守护该路径。
func TestSubscribe_NilPool_ReturnsErrorNotPanic(t *testing.T) {
	b := newNilPoolBroker(t)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil pool 下 Subscribe panic（缺防护，可能来自 ensureGroup）: %v", r)
		}
	}()
	sub, err := b.Subscribe("s.nilpool",
		func(context.Context, broker.Event) error { return nil },
		func() any { var x []byte; return &x })
	if err == nil {
		_ = sub.Unsubscribe(false)
		t.Fatal("nil pool 下 Subscribe 应返回 error")
	}
	t.Logf("Subscribe 正确报错: %v", err)
}

// TestDisconnect_NilPool_NoPanic —— Disconnect 在 nil pool 下不得 panic（既有防护）。
func TestDisconnect_NilPool_NoPanic(t *testing.T) {
	b := newNilPoolBroker(t)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil pool 下 Disconnect panic（缺防护）: %v", r)
		}
	}()
	if err := b.Disconnect(); err != nil {
		t.Fatalf("Disconnect 应安全返回 nil，实得 %v", err)
	}
}
