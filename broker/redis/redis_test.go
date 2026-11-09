package redis_test

import (
	"testing"
	"time"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/redis"
)

// TestNewBroker 覆盖 switch 三分支：Stream / PubSub / default（非法串回落到 pubsub）。
// NewBroker 只构造对象、不建连接，故本机无 redis 也能断言非 nil。
func TestNewBroker(t *testing.T) {
	cases := []redis.DriverType{
		redis.DriverTypeStream,
		redis.DriverTypePubSub,
		redis.DriverType("bogus"), // 走 default 分支
	}
	for _, dt := range cases {
		if b := redis.NewBroker(dt); b == nil {
			t.Fatalf("NewBroker(%q) 返回 nil", string(dt))
		}
	}
	// 带 broker.Option 的构造同样可用。
	if b := redis.NewBroker(redis.DriverTypePubSub, broker.WithAddress("127.0.0.1:6379")); b == nil {
		t.Fatal("NewBroker 带地址返回 nil")
	}
}

// TestCommonOptionsForward 逐个调用 broker.Option 转发函数并施加到 Options，
// 验证不 panic（转发正确性由 option 包自身用例保证，这里覆盖 redis 包装行）。
func TestCommonOptionsForward(t *testing.T) {
	opts := []broker.Option{
		redis.WithConnectTimeout(time.Second),
		redis.WithReadTimeout(time.Second),
		redis.WithWriteTimeout(time.Second),
		redis.WithIdleTimeout(time.Second),
		redis.WithMaxIdle(8),
		redis.WithMaxActive(16),
		redis.WithDefaultOptions(),
	}
	var o broker.Options
	for _, f := range opts {
		f(&o)
	}
	if o.Context == nil {
		t.Fatal("施加选项后 Context 仍为 nil")
	}
}

// TestStreamOptionsForward 覆盖 Stream 专属订阅/发布选项转发：调用 redis 包装并施加。
func TestStreamOptionsForward(t *testing.T) {
	var so broker.SubscribeOptions
	redis.WithStreamGroup("g")(&so)
	redis.WithStreamConsumer("c")(&so)
	redis.WithStreamBlockTime(time.Second)(&so)
	redis.WithStreamCount(5)(&so)
	if so.Context == nil {
		t.Fatal("订阅选项施加后 Context 仍为 nil")
	}

	var po broker.PublishOptions
	redis.WithStreamMaxLen(100)(&po)
	if po.Context == nil {
		t.Fatal("发布选项施加后 Context 仍为 nil")
	}
}
