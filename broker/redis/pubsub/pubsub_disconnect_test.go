package pubsub

// pubsub_disconnect_test.go —— Disconnect 在 pool 为 nil 时不得 panic。

import (
	"testing"

	"github.com/kalandramo/bald/broker"
)

// TestDisconnect_AfterFailedConnect_NoPanic —— Connect 失败后清理不得 panic。
//
// Connect 探活失败会丢弃 pool（`b.pool = nil`）；若 Disconnect 直接
// `b.pool.Close()`，调用即 `(*Pool)(nil).Close()` **panic**。
// stream driver 同此 nil 防护约定。
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
	// 关键断言：此后 Disconnect 不得 panic。
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Connect 失败后 Disconnect panic（nil pool 未防护）: %v", r)
		}
	}()
	if err := b.Disconnect(); err != nil {
		t.Fatalf("Disconnect 应安全返回 nil，实得 %v", err)
	}
}
