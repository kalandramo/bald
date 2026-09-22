package pubsub

// pubsub_connect_test.go —— D13.2 回归：Connect 必须对不可达 Redis 返回 error。

import (
	"testing"

	"github.com/kalandramo/bald/broker"
)

// TestConnect_FailsWhenUnreachable —— D13.2（框架缺陷报告）：redigo 的 Pool
// 是懒连接，修复前 Connect 只建 pool 不探活，Redis 不可达时返回 nil（假成功），
// 故障推迟到首次 Publish/Subscribe 才暴露，误导启动期健康检查。
//
// 修复后：Connect 取一条连接 PING，失败即返回 error 并丢弃 pool。
func TestConnect_FailsWhenUnreachable(t *testing.T) {
	// 用一个几乎必然不可达的端口（避开 6379 上可能存在的真实 Redis）。
	b := NewBroker(broker.WithAddress("redis://127.0.0.1:6399"))
	if err := b.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := b.Connect(); err == nil {
		t.Fatalf("Connect 对不可达 Redis 返回 nil（假成功，D13.2 未修）")
	}

	// 失败后应可重试（pool 已丢弃，addr 保留）。
	if b.Address() == "" {
		t.Fatalf("Connect 失败后 addr 被清空——重试将无从连接")
	}
}
