package rocketmq

import (
	"testing"

	"github.com/kalandramo/bald/broker"
)

// fakeSubscriber 实现 broker.Subscriber，记录 Unsubscribe 调用。
type fakeSubscriber struct {
	topic     string
	unsubs    int
	unsubArgs []bool
}

func (f *fakeSubscriber) Options() broker.SubscribeOptions { return broker.SubscribeOptions{} }
func (f *fakeSubscriber) Topic() string                    { return f.topic }
func (f *fakeSubscriber) Unsubscribe(removeFromManager bool) error {
	f.unsubs++
	f.unsubArgs = append(f.unsubArgs, removeFromManager)
	return nil
}

// TestDisconnect_ClosesSubscribers Disconnect 必须注销并清空全部订阅者。
//
// 缺陷：Disconnect 此前只 Shutdown producers，完全不管 b.subscribers——
// consumer 与其消费 goroutine 在停机时不被注销（对齐 kafka 的
// `b.subscribers.Clear()` 才是正确范式）。字段 subscribers 归一化存在却没人清。
func TestDisconnect_ClosesSubscribers(t *testing.T) {
	b := NewBroker().(*rocketmqBroker)
	b.connected = true

	s1 := &fakeSubscriber{topic: "t1"}
	s2 := &fakeSubscriber{topic: "t2"}
	b.subscribers.Add("t1", s1)
	b.subscribers.Add("t2", s2)
	if got := b.subscribers.Len(); got != 2 {
		t.Fatalf("precondition: subscribers = %d, want 2", got)
	}

	if err := b.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if s1.unsubs != 1 || s2.unsubs != 1 {
		t.Fatalf("subscribers Unsubscribe calls = (%d,%d), want (1,1)", s1.unsubs, s2.unsubs)
	}
	if got := b.subscribers.Len(); got != 0 {
		t.Fatalf("subscribers after Disconnect = %d, want 0（consumer 未注销会泄漏）", got)
	}
}

// TestDisconnect_Idempotent 重复 Disconnect 幂等（connected=false 后早退）。
func TestDisconnect_Idempotent(t *testing.T) {
	b := NewBroker().(*rocketmqBroker)
	b.connected = true
	s := &fakeSubscriber{topic: "t"}
	b.subscribers.Add("t", s)

	if err := b.Disconnect(); err != nil {
		t.Fatalf("first Disconnect: %v", err)
	}
	if err := b.Disconnect(); err != nil {
		t.Fatalf("second Disconnect must be no-op, got: %v", err)
	}
	if s.unsubs != 1 {
		t.Fatalf("Unsubscribe calls = %d, want 1 (second Disconnect must not re-run)", s.unsubs)
	}
}
