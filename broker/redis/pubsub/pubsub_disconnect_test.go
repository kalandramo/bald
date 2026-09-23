package pubsub

// pubsub_disconnect_test.go —— Disconnect 在 pool 为 nil 时不得 panic。

import (
	"testing"

	"github.com/kalandramo/bald/broker"
)

// TestDisconnect_AfterFailedConnect_NoPanic —— D13.2 修复引入的回归防护。
//
// 背景：D13.2 让 Connect 在探活失败时丢弃 pool（`b.pool = nil`），但当时
// pubsub 的 Disconnect 直接 `b.pool.Close()`，无 nil 检查 → 调用即
// `(*Pool)(nil).Close()` **panic**。stream driver 一直有该防护，pubsub 缺失。
//
// 本测试锁定：Connect 失败后调用 Disconnect 必须安全返回（不 panic）。
func TestDisconnect_AfterFailedConnect_NoPanic(t *testing.T) {
	b := NewBroker(broker.WithAddress("redis://127.0.0.1:6399")) // 不可达端口
	if err := b.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Connect 必然失败（不可达），失败路径会丢弃 pool。
	if err := b.Connect(); err == nil {
		t.Fatalf("Connect 对不可达 Redis 应返回 error")
	}
	// 关键断言：此后 Disconnect 不得 panic（修复前此处 panic）。
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Connect 失败后 Disconnect panic（nil pool 未防护）: %v", r)
		}
	}()
	if err := b.Disconnect(); err != nil {
		t.Fatalf("Disconnect 应安全返回 nil，实得 %v", err)
	}
}
