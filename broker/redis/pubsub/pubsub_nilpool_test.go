package pubsub

// pubsub_nilpool_test.go —— pool 为 nil 时的防护（缺陷族守护）。
//
// ## 缺陷族（2026-09-23 勘探）
//
// `b.pool = nil` 表示「未连接」状态，有三条路径会置 nil：
//   1. 从未 Connect
//   2. Connect 探活失败（D13.2 起，v0.1.1）
//   3. 已 Disconnect
//
// 而 publish/Subscribe 直接 `b.pool.Get()` → `(*Pool)(nil).Get()`
// **panic: invalid memory address or nil pointer dereference**。
//
// 归因：v0.1.0 起 Disconnect 即置 nil，故路径 1/3 一直可触发（既有缺陷）；
// D13.2 让 Connect 失败也置 nil，**扩大了触发面**（无需先 Disconnect）。
// 框架内 publication.go / subscriber.go 已有 `if pool == nil` 防护先例，
// 但 publish/Subscribe 两处遗漏。
//
// 本测试锁定：nil pool 下 publish/Subscribe 必须返回 error 而非 panic。

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/broker"
)

// newNilPoolBroker 返回一个 pool 为 nil 的 broker。
// 用不可达地址 Connect 使其探活失败并丢弃 pool（路径 2），
// 这是最贴近真实误用的构造方式。
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
	if pb, ok := b.(*pubsubBroker); ok && pb.pool != nil {
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
	err := b.Publish(context.Background(), "t.nilpool",
		&broker.Message{Body: []byte("x")})
	if err == nil {
		t.Fatal("nil pool 下 Publish 应返回 error")
	}
	t.Logf("Publish 正确报错: %v", err)
}

// TestSubscribe_NilPool_ReturnsErrorNotPanic —— Subscribe 在 nil pool 下不得 panic。
func TestSubscribe_NilPool_ReturnsErrorNotPanic(t *testing.T) {
	b := newNilPoolBroker(t)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil pool 下 Subscribe panic（缺防护）: %v", r)
		}
	}()
	sub, err := b.Subscribe("t.nilpool",
		func(context.Context, broker.Event) error { return nil },
		func() any { var x []byte; return &x })
	if err == nil {
		_ = sub.Unsubscribe(false)
		t.Fatal("nil pool 下 Subscribe 应返回 error")
	}
	t.Logf("Subscribe 正确报错: %v", err)
}

// TestDisconnect_NilPool_NoPanic —— Disconnect 在 nil pool 下不得 panic。
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
