package rocketmq

// subscriber_test.go —— 订阅者生命周期与 broker 注册表的接线（风险登记册 §7.24 的
// 两处生产缺陷在这里判）。
//
// 形状照 kafka 的同名闸门（broker/kafka/subscriber_test.go）：Subscribe 把订阅者收进
// `b.subscribers`，摘除的责任在调用方（`Unsubscribe(true)`）或 `Disconnect` 的 Clear()。
// rocketmq 这一族原先两条都不成立——
//   - `&subscriber{}` 少填 `r`，于是 `Unsubscribe(true)` 走到 `s.r.subscribers` 当场 nil 解引用；
//   - `Subscribe` 从不 `b.subscribers.Add`，注册表永远是空的，`Disconnect` 里的
//     `Clear()`（注释写着「对齐 kafka 的 subscribers.Clear() 范式」）对着空表什么也不做。
//
// 两条都是本轮修的，判据在这里。为什么不能只在 contract 那族闸门里判：那八行映射的
// 读数全在**线上请求**里，而这两条是**进程内的簿记**，线上看不出来。

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/rocketmq/option"
)

// newSubscribableBroker 造一台「已连接、名字服务会答路由」的 broker。
// tag 参与 instance_name：SDK 的 rmqClient 按 `ip@instanceName` 进程内共享，
// 两条腿撞同一个 instance 时会复用前一条腿的名字服务客户端——那条腿的假线一关，
// 本条腿就再也取不到路由，报错还会伪装成「Subscribe 坏了」。
func newSubscribableBroker(t *testing.T, tag string) *rocketmqBroker {
	t.Helper()

	nameServer := startRouteFake(t)

	b, ok := NewBroker(
		option.WithNameServer([]string{nameServer}),
		option.WithGroupName("GID-"+tag),
		option.WithInstanceName(tag+"-"+strconv.FormatInt(time.Now().UnixNano(), 10)),
		option.WithRetryCount(1),
	).(*rocketmqBroker)
	if !ok {
		t.Fatal("NewBroker 返回了非 *rocketmqBroker")
	}
	if err := b.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := b.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// 注册在假线之后 ⇒ t.Cleanup 的 LIFO 让 Disconnect 先跑，停机时假线还活着。
	t.Cleanup(func() { _ = b.Disconnect() })

	return b
}

// mustNotPanic 把 panic 报成判红而不是让测试二进制崩掉——崩掉同样是红，
// 但会连带杀掉同包里后面的用例，把一处缺陷放大成一屏噪声。
func mustNotPanic(t *testing.T, op string, fn func()) {
	t.Helper()

	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("%s panic: %v", op, p)
		}
	}()
	fn()
}

func noopHandler(context.Context, broker.Event) error { return nil }

// TestSubscribe_RegistersSubscriber 判第二条缺陷：订阅成功后注册表里必须有这一条，
// 而且表里那个就是返回给调用方的订阅者。
//
// 「Add」这一步唯一的作用是让 Disconnect 的 Clear() 找得到 consumer——表空时清理是
// 空转，订阅者只增不减。身份判据（Get == sub）而不是只判 Has：把上一个仍活着的订阅者
// 覆盖进同一条目也会让 Has 变绿。
func TestSubscribe_RegistersSubscriber(t *testing.T) {
	b := newSubscribableBroker(t, "reg")

	const topic = "v2-lifecycle-register"
	sub, err := b.Subscribe(topic, noopHandler, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if got := b.subscribers.Get(topic); got != sub {
		t.Fatalf("注册表里 topic=%q 的订阅者是 %v, want 返回给调用方的那一个", topic, got)
	}
	if n := b.subscribers.Len(); n != 1 {
		t.Fatalf("注册表 %d 条, want 1", n)
	}
}

// TestSubscriberUnsubscribe_RemovesFromBroker 判第一条缺陷加摘除语义：
// `Unsubscribe(true)` 既不 panic（`r` 必须非空），也要把条目从注册表里摘掉。
func TestSubscriberUnsubscribe_RemovesFromBroker(t *testing.T) {
	b := newSubscribableBroker(t, "unsub")

	const topic = "v2-lifecycle-unsubscribe"
	if _, err := b.Subscribe(topic, noopHandler, nil); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	sub := b.subscribers.Get(topic)
	if sub == nil {
		t.Fatal("前置条件不成立：注册表里没有该 topic")
	}

	mustNotPanic(t, "Unsubscribe(true)", func() {
		if err := sub.Unsubscribe(true); err != nil {
			t.Errorf("Unsubscribe(true): %v", err)
		}
	})

	if b.subscribers.Has(topic) {
		t.Fatal("Unsubscribe(true) 后注册表仍留着该条目——调用方以为摘掉了，Disconnect 会再清一次")
	}
	if live, ok := sub.(*subscriber); !ok || !live.IsClosed() {
		t.Fatal("订阅者自身没有进入已关闭状态")
	}
}

// TestSubscriberUnsubscribe_KeepsEntryWhenNotRemoving 钉住摘除责任的另一半：
// `removeFromManager=false`（Clear() 用的就是这一支）只释放订阅关系，不动注册表。
// 这一支之所以要有判据，是因为两个布尔值走的是同一个函数体，
// 把条件写成 `if !removeFromManager` 这种翻转在「Disconnect 之后表也空了」的观察下看不出差别。
func TestSubscriberUnsubscribe_KeepsEntryWhenNotRemoving(t *testing.T) {
	b := newSubscribableBroker(t, "keep")

	const topic = "v2-lifecycle-keep"
	sub, err := b.Subscribe(topic, noopHandler, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	mustNotPanic(t, "Unsubscribe(false)", func() {
		if err := sub.Unsubscribe(false); err != nil {
			t.Errorf("Unsubscribe(false): %v", err)
		}
	})

	if !b.subscribers.Has(topic) {
		t.Fatal("Unsubscribe(false) 不该摘除注册表条目——摘除是调用方或 Disconnect 的责任")
	}
}

// TestDisconnect_ClearsLiveSubscriber 判「Disconnect 真的能注销活着的订阅者」：
// 表里有条目、Clear 走到真实 consumer 的 Unsubscribe 且全程不 panic。
//
// 这是本轮 Add 修复的端到端一步：只判表里有东西（TestSubscribe_RegistersSubscriber）
// 不够——Clear() 拿到的是 broker.Subscriber 接口，如果注册的实例本身不对
// （比如误注册了一个零值 *subscriber），panic 会在 Disconnect 时才炸出来。
func TestDisconnect_ClearsLiveSubscriber(t *testing.T) {
	b := newSubscribableBroker(t, "clear")

	const topic = "v2-lifecycle-disconnect"
	sub, err := b.Subscribe(topic, noopHandler, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	mustNotPanic(t, "Disconnect", func() {
		if err := b.Disconnect(); err != nil {
			t.Errorf("Disconnect: %v", err)
		}
	})

	if n := b.subscribers.Len(); n != 0 {
		t.Fatalf("Disconnect 后注册表 %d 条, want 0", n)
	}
	live, ok := sub.(*subscriber)
	if !ok {
		t.Fatalf("Subscribe 返回了 %T，不是 *subscriber", sub)
	}
	if !live.IsClosed() {
		t.Fatal("Disconnect 没有走到这个订阅者的 Unsubscribe——consumer 侧的订阅关系还挂着")
	}
}
