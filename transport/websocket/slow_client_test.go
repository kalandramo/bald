package websocket

import (
	"net/url"
	"testing"
	"time"
)

// TestSendMessage_SlowClientKicked 复现慢客户端挂死：此前 SendMessage 是
// select{done,send} 无界阻塞——写泵被 TCP 背压卡死、缓冲打满后，一个卡死的
// 客户端把 Broadcast 与调用方 goroutine 一起挂死。修复后缓冲满进超时路径，
// 超时按慢客户端关闭并注销该会话（sse 的 trySend 踢除范式的 websocket 实现）。
func TestSendMessage_SlowClientKicked(t *testing.T) {
	client, server := wsPair(t)
	defer func() { _ = client.Close() }()
	srv := NewServer(":0")
	s := NewSession(srv, server, url.Values{})
	s.sendTimeout = 50 * time.Millisecond // 白盒注入小窗口
	// 不调用 Listen()：写泵不消费，缓冲必然打满。

	for i := 0; i < channelBufSize; i++ {
		s.SendMessage([]byte("fill"))
	}

	// 下一条进入超时路径：修复前这里无界阻塞（看门狗抓死锁）。
	done := make(chan struct{})
	go func() {
		s.SendMessage([]byte("overflow"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SendMessage 对满缓冲无界阻塞（慢客户端挂死调用方）")
	}

	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("超时后慢客户端应被关闭踢除")
	}
}

// TestSendMessage_HealthyFastPathUnaffected 对照：缓冲有空位时直达入队，
// 不进超时路径、会话存活（健康客户端零开销语义保持）。
func TestSendMessage_HealthyFastPathUnaffected(t *testing.T) {
	client, server := wsPair(t)
	defer func() { _ = client.Close() }()
	srv := NewServer(":0")
	s := NewSession(srv, server, url.Values{})
	s.sendTimeout = 50 * time.Millisecond

	s.SendMessage([]byte("hello"))
	select {
	case <-s.done:
		t.Fatal("健康客户端不得被踢除")
	default:
	}
}

// TestWithSendTimeout_NegativePanics 构造期 fail-fast：非正超时在装配期就报。
func TestWithSendTimeout_NegativePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("WithSendTimeout(0) should panic")
		}
	}()
	NewServer(":0", WithSendTimeout(0))
}
