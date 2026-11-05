package rocketmq

import (
	"sync"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/kalandramo/bald/broker"
)

type subscriber struct {
	sync.RWMutex

	r       *rocketmqBroker
	topic   string
	options broker.SubscribeOptions
	handler broker.Handler
	reader  rocketmq.PushConsumer
	closed  bool
}

func (s *subscriber) Options() broker.SubscribeOptions {
	s.RLock()
	defer s.RUnlock()

	return s.options
}

func (s *subscriber) Topic() string {
	s.RLock()
	defer s.RUnlock()

	return s.topic
}

// Unsubscribe 释放订阅关系并**停机**这个 consumer。
//
// 停机这一步是必须的：SDK 的 pushConsumer.Unsubscribe 只删订阅表项
// （consumer/push_consumer.go 里直接 `subscriptionDataTable.Delete(topic)` 后 `return nil`），
// 消费线程、拉取线程、rmqClient 的心跳线程全都照跑。调用方以为摘干净了，进程里却还
// 挂着整套后台线程和到 broker 的连接——本机假线实测：不 Shutdown 时假 broker 每 3s
// 多收到一条请求（+3002ms 一条 code=38 注销尝试、+6004ms 一条 code=34 心跳），
// 这一条就是 contract/teardown_offline_test.go 的流水冻结判据读的东西。
// pushConsumer.Shutdown 由 closeOnce 兜住，重复调用安全。
//
// removeFromManager 只决定是否摘 bald 自己的注册表条目：Clear() 已经自己清过表，
// 走的是 false 这一支；调用方主动摘除走 true。
func (s *subscriber) Unsubscribe(removeFromManager bool) error {
	s.Lock()
	defer s.Unlock()

	err := s.reader.Unsubscribe(s.topic)

	s.closed = true

	shutdownErr := s.reader.Shutdown()

	if removeFromManager {
		_ = s.r.subscribers.RemoveOnly(s.topic)
	}

	if err != nil {
		return err
	}
	return shutdownErr
}

func (s *subscriber) IsClosed() bool {
	s.RLock()
	defer s.RUnlock()

	return s.closed
}
