package sse

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// syncBuffer 是并发安全的日志捕获缓冲（log 写入与测试读取可能并发）。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLog 把标准日志重定向到缓冲区，测试结束恢复。
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

// recvEvent 在超时内读取一个事件；通道被关闭或超时即失败。
func recvEvent(t *testing.T, ch <-chan *Event, timeout time.Duration) *Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("subscriber connection closed unexpectedly")
		}
		return ev
	case <-time.After(timeout):
		t.Fatal("timed out waiting for event")
		return nil
	}
}

// eventually 轮询条件直到成立或超时。
func eventually(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// TestStream_SlowSubscriberDoesNotBlockOthers 锁定：单个不消费的订阅者写满
// 缓冲后被移出断开，其他订阅者仍能收到全部事件。
func TestStream_SlowSubscriberDoesNotBlockOthers(t *testing.T) {
	logs := captureLog(t)
	srv := NewServer(":0")
	stream := srv.CreateStream("slow")
	t.Cleanup(func() { srv.RemoveStream("slow") })

	slow := stream.addSubscriber("", nil)
	fast := stream.addSubscriber("", nil)

	const total = 200
	go func() {
		for i := 0; i < total; i++ {
			srv.Publish(context.Background(), "slow", &Event{Data: []byte(fmt.Sprintf("e%d", i))})
		}
	}()

	for i := 0; i < total; i++ {
		ev := recvEvent(t, fast.connection, 3*time.Second)
		if want := fmt.Sprintf("e%d", i); string(ev.Data) != want {
			t.Fatalf("fast subscriber event %d = %q, want %q", i, ev.Data, want)
		}
	}

	eventually(t, time.Second, func() bool { return stream.getSubscriberCount() == 1 })
	eventually(t, time.Second, func() bool {
		return strings.Contains(logs.String(), "disconnect slow subscriber")
	})

	// 慢订阅者通道应已关闭；缓冲内残留事件可先读完，但必须最终结束。
	closed := make(chan struct{})
	go func() {
		for range slow.connection {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("slow subscriber was not disconnected")
	}
}

// TestStream_SlowSubscriberUnsubscribeOnce 锁定：慢客户端被策略移除与随后
// 请求结束的注销不会重复触发 onUnsubscribe。
func TestStream_SlowSubscriberUnsubscribeOnce(t *testing.T) {
	var calls int32
	srv := NewServer(":0", WithUnSubscriberFunction(func(StreamID, *Subscriber) {
		atomic.AddInt32(&calls, 1)
	}))
	stream := srv.CreateStream("slow-once")
	t.Cleanup(func() { srv.RemoveStream("slow-once") })

	slow := stream.addSubscriber("", nil)
	for i := 0; i < defaultSubscriberBuffer+1; i++ {
		srv.Publish(context.Background(), "slow-once", &Event{Data: []byte("x")})
	}
	eventually(t, time.Second, func() bool { return stream.getSubscriberCount() == 0 })

	slow.close() // 请求结束路径：已被移除，不得再次回调
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("onUnsubscribe calls = %d, want 1", got)
	}
}

// TestStream_ReplayKeepsBoundedWindow 锁定：回放日志只保留最近 N 条。
func TestStream_ReplayKeepsBoundedWindow(t *testing.T) {
	srv := NewServer(":0", WithEventLogSize(2))
	stream := srv.CreateStream("replay")
	t.Cleanup(func() { srv.RemoveStream("replay") })

	first := stream.addSubscriber("", nil)
	const total = 5
	for i := 0; i < total; i++ {
		srv.Publish(context.Background(), "replay", &Event{Data: []byte(fmt.Sprintf("e%d", i))})
	}
	for i := 0; i < total; i++ {
		ev := recvEvent(t, first.connection, time.Second)
		if want := fmt.Sprintf("e%d", i); string(ev.Data) != want {
			t.Fatalf("event %d = %q, want %q", i, ev.Data, want)
		}
	}

	second := stream.addSubscriber("", nil)
	for _, want := range []string{"e3", "e4"} {
		ev := recvEvent(t, second.connection, time.Second)
		if string(ev.Data) != want {
			t.Fatalf("replayed %q, want %q", ev.Data, want)
		}
	}
	select {
	case ev := <-second.connection:
		t.Fatalf("unexpected extra replay %q (log should keep only 2 events)", ev.Data)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestStream_AddSubscriberAfterClose 锁定：流关闭后订阅与注销都不再阻塞。
func TestStream_AddSubscriberAfterClose(t *testing.T) {
	srv := NewServer(":0")
	stream := srv.CreateStream("closed")
	srv.RemoveStream("closed")

	done := make(chan *Subscriber, 1)
	go func() { done <- stream.addSubscriber("", nil) }()

	var sub *Subscriber
	select {
	case sub = <-done:
	case <-time.After(time.Second):
		t.Fatal("addSubscriber blocked after stream closed")
	}

	select {
	case _, ok := <-sub.connection:
		if ok {
			t.Fatal("subscription of closed stream should have a closed connection")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription of closed stream did not terminate")
	}

	closed := make(chan struct{})
	go func() {
		sub.close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Subscriber.close blocked after stream closed")
	}
}

func TestEventLog_Trim(t *testing.T) {
	var log EventLog
	for i := 0; i < 5; i++ {
		log.Add(&Event{Data: []byte(fmt.Sprintf("e%d", i))})
	}

	log.Trim(2)
	if len(log) != 2 {
		t.Fatalf("length after Trim(2) = %d, want 2", len(log))
	}
	if string(log[0].Data) != "e3" || string(log[1].Data) != "e4" {
		t.Fatalf("retained events = %q,%q, want e3,e4", log[0].Data, log[1].Data)
	}

	log.Trim(0) // 0 表示显式不裁剪
	if len(log) != 2 {
		t.Fatalf("Trim(0) changed length to %d, want 2", len(log))
	}
}

func TestEventLog_Since(t *testing.T) {
	var log EventLog
	log.Add(&Event{Data: []byte("a")})
	log.Add(&Event{Data: []byte("b")})

	if all := log.Since(""); len(all) != 2 {
		t.Fatalf("Since(\"\") length = %d, want 2", len(all))
	}
	since := log.Since(string(log[1].ID))
	if len(since) != 1 || string(since[0].Data) != "b" {
		t.Fatalf("Since(last) = %d events, want only \"b\"", len(since))
	}
}

// TestEventLog_TruncatedBefore 锁定：能识别 Last-Event-ID 早于保留窗口。
func TestEventLog_TruncatedBefore(t *testing.T) {
	var log EventLog
	for i := 0; i < 3; i++ {
		log.Add(&Event{Data: []byte(fmt.Sprintf("e%d", i))})
	}
	dropped := string(log[0].ID)
	log.Trim(2)
	retainedFirst := string(log[0].ID)

	if log.TruncatedBefore("") {
		t.Fatal("empty Last-Event-ID must not count as truncated")
	}
	if log.TruncatedBefore(retainedFirst) {
		t.Fatal("Last-Event-ID at window head must not count as truncated")
	}
	if !log.TruncatedBefore(dropped) {
		t.Fatal("dropped Last-Event-ID should be reported as outside the retained window")
	}
	if log.TruncatedBefore("ffffffffffffffffffffffffffffffff") {
		t.Fatal("Last-Event-ID newer than retained events must not count as truncated")
	}
}

// TestStream_ReplayLogsGap 锁定：Last-Event-ID 早于回放窗口时留下日志，
// 便于从服务端确认客户端丢事件的区间。
func TestStream_ReplayLogsGap(t *testing.T) {
	logs := captureLog(t)
	srv := NewServer(":0", WithEventLogSize(1))
	stream := srv.CreateStream("gap")
	t.Cleanup(func() { srv.RemoveStream("gap") })

	first := stream.addSubscriber("", nil)
	srv.Publish(context.Background(), "gap", &Event{Data: []byte("e0")})
	recvEvent(t, first.connection, time.Second)
	srv.Publish(context.Background(), "gap", &Event{Data: []byte("e1")})
	recvEvent(t, first.connection, time.Second)

	// 窗口仅保留 e1；用明显更早的 ID 订阅会触发截断告警。
	second := stream.addSubscriber("00000000000000000000000000000000", nil)
	if ev := recvEvent(t, second.connection, time.Second); string(ev.Data) != "e1" {
		t.Fatalf("replayed %q, want e1", ev.Data)
	}
	eventually(t, time.Second, func() bool {
		return strings.Contains(logs.String(), "replay gap")
	})
}
