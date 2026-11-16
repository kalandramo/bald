package rabbitmq

import (
	"net"
	"testing"
	"time"

	"github.com/kalandramo/bald/broker"
)

// TestWaitConnectionLifecycle 钉住 waitConnection 的状态机：电平锁存语义。
// 修复前 connect() 裸 close 已关闭的通道，Connect→Close→Connect 或并发 Connect
// 触发 close of closed channel panic；修复后 signalConnectedLocked 以
// select-default 判重，锁存语义（connected 期间通道保持关闭、晚到等待方立即放行）
// 与防 panic 两者兼得。
func TestWaitConnectionLifecycle(t *testing.T) {
	r := newRabbitMQConnection(broker.NewOptionsAndApply())

	assertOpen := func(stage string) {
		t.Helper()
		select {
		case <-r.waitChan():
			t.Fatalf("%s：等待通道不应处于已关闭态", stage)
		default:
		}
	}
	assertClosed := func(stage string) {
		t.Helper()
		select {
		case <-r.waitChan():
		default:
			t.Fatalf("%s：等待通道应处于锁存的关闭态", stage)
		}
	}

	assertOpen("初始")

	// 模拟连接建立：signal 关闭并锁存。
	r.Lock()
	r.signalConnectedLocked()
	r.Unlock()
	assertClosed("signal 后（锁存）")

	// 重复 signal：并发 Connect 场景不再 panic，状态不变。
	r.Lock()
	r.signalConnectedLocked()
	r.Unlock()
	assertClosed("重复 signal 后")

	// 锁存语义回归测试：signal 之后才启动的等待方必须立即放行。
	// （这是 Start 后才起 resubscribe 的订阅者的正常消费路径；边沿广播式的
	// 实现会让它阻塞到下一次断线重连。）
	late := make(chan struct{})
	go func() {
		select {
		case <-r.waitChan():
			close(late)
		case <-time.After(100 * time.Millisecond):
		}
	}()
	select {
	case <-late:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("锁存后的晚到等待方 100ms 内未放行")
	}

	// Close：等待通道维持锁存关闭态（既有语义，Close 不动它）；
	// 被唤醒的等待方经 isConnected 复核，并经 r.close 退出。
	if err := r.Close(); err == nil {
		t.Fatal("Connection 为 nil 时 Close 应报错")
	}
	assertClosed("Close 后")

	// 公共 API 的 Connect→Close→Connect 循环：全程不允许 panic，指向
	// 无人监听地址的 Connect 每次都如实报错。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	refused := "amqp://" + ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	r2 := newRabbitMQConnection(broker.NewOptionsAndApply(broker.WithAddress(refused)))
	for i := 0; i < 3; i++ {
		if err := r2.Connect(false, nil); err == nil {
			t.Fatalf("第 %d 次 Connect 指向假地址却成功了", i+1)
		}
		if err := r2.Close(); err == nil {
			t.Fatalf("第 %d 次 Close（连接已断）应报错", i+1)
		}
	}
}
