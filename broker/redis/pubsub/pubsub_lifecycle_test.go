package pubsub

import (
	"context"
	"testing"
	"time"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/encoding"
	jsoncodec "github.com/kalandramo/bald/encoding/json"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestBroker 构造已 Init+Connect 的 pubsub broker（miniredis 后端）。
// 编码器显式注册（默认 codec 惰性查表——测试二进制里没人注册就查不到）。
func init() {
	encoding.MustRegister(jsoncodec.New())
}

func newTestBroker(t *testing.T, mr *miniredis.Miniredis, opts ...broker.Option) *pubsubBroker {
	t.Helper()
	bb, ok := NewBroker(append([]broker.Option{broker.WithAddress(mr.Addr())}, opts...)...).(*pubsubBroker)
	require.True(t, ok, "NewBroker 应返回具体类型")
	require.NoError(t, bb.Init())
	require.NoError(t, bb.Connect())
	t.Cleanup(func() { _ = bb.Disconnect() })
	return bb
}

// TestPubSub_PublishSubscribeRoundtrip 回环：deferred 语义不存在（broker 直连），
// 订阅后发布，raw handler 收到 []byte。
func TestPubSub_PublishSubscribeRoundtrip(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroker(t, mr)

	got := make(chan []byte, 1)
	sub, err := b.Subscribe("topic.raw", func(_ context.Context, evt broker.Event) error {
		got <- evt.Message().Body.([]byte)
		return nil
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, sub)

	// 等订阅生效（SUBSCRIBE 确认回包被 recv 循环消费）。
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, b.Publish(context.Background(), "topic.raw", &broker.Message{Body: "hello"}))

	select {
	case v := <-got:
		// 线上字节 = codec 编码后的形态：发布经 Marshal（json 给字符串加引号）。
		assert.Equal(t, []byte(`"hello"`), v)
	case <-time.After(3 * time.Second):
		t.Fatal("raw 订阅未收到消息")
	}
	assert.Equal(t, "topic.raw", sub.Topic())
}

// TestPubSub_TypedBinderRoundtrip 带 binder：Body 经 codec 落地成 *payload。
func TestPubSub_TypedBinderRoundtrip(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroker(t, mr)

	type payload struct {
		Value string `json:"value"`
	}
	got := make(chan string, 1)
	_, err := b.Subscribe("topic.typed", func(_ context.Context, evt broker.Event) error {
		p, ok := evt.Message().Body.(*payload)
		if !ok {
			t.Errorf("body type = %T, want *payload", evt.Message().Body)
			return nil
		}
		got <- p.Value
		return nil
	}, func() any { return new(payload) })
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)
	require.NoError(t, b.Publish(context.Background(), "topic.typed", &broker.Message{Body: payload{Value: "typed"}}))

	select {
	case v := <-got:
		assert.Equal(t, "typed", v)
	case <-time.After(3 * time.Second):
		t.Fatal("typed 订阅未收到消息")
	}
}

// TestPubSub_NotConnectedGuards 未 Connect 时 Publish/Subscribe 前置防护（不 panic）。
func TestPubSub_NotConnectedGuards(t *testing.T) {
	b := NewBroker()
	bb := b.(*pubsubBroker)
	require.NoError(t, bb.Init())

	err := bb.Publish(context.Background(), "t", &broker.Message{Body: "x"})
	require.ErrorContains(t, err, "not connected")

	_, err = bb.Subscribe("t", func(context.Context, broker.Event) error { return nil }, nil)
	require.ErrorContains(t, err, "not connected")
}

// TestPubSub_ConnectRefusedProbe 拒连端口：PING 探针把「懒连接假成功」提前到
// Connect 的返回值，且 pool 被丢弃（二次 Connect 仍失败、再次丢弃）。
func TestPubSub_ConnectRefusedProbe(t *testing.T) {
	bb, ok := NewBroker().(*pubsubBroker)
	require.True(t, ok)
	require.NoError(t, bb.Init())
	bb.options.Addrs = []string{"127.0.0.1:1"} // 拒连端口

	require.ErrorContains(t, bb.Connect(), "redis: connect:", "PING 探针必须暴露拒连")
	assert.Nil(t, bb.pool, "探活失败必须丢弃 pool")

	require.Error(t, bb.Connect(), "pool 已丢弃，二次 Connect 重建后仍探活失败")
}

// TestPubSub_InitNormalizesAddress 地址归一化三态：裸 host:port 补 scheme、
// 空 addr 落默认、完整 URL 原样。
func TestPubSub_InitNormalizesAddress(t *testing.T) {
	mk := func(addrs []string) *pubsubBroker {
		b := NewBroker()
		bb := b.(*pubsubBroker)
		bb.options.Addrs = addrs
		require.NoError(t, bb.Init())
		return bb
	}

	assert.Equal(t, "redis://127.0.0.1:6379", mk(nil).addr, "空 addr 落默认")
	assert.Equal(t, "redis://127.0.0.1:6379", mk([]string{""}).addr, "空串 addr 落默认")
	assert.Equal(t, "redis://10.0.0.1:7000", mk([]string{"10.0.0.1:7000"}).addr, "裸 host:port 补 redis://")
	assert.Equal(t, "redis://10.0.0.1:7000", mk([]string{"redis://10.0.0.1:7000"}).addr, "完整 URL 原样")
}

// TestPubSub_InitWhileConnected 已连接时 Init 报错（生命周期门）。
func TestPubSub_InitWhileConnected(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroker(t, mr)

	err := b.Init()
	require.ErrorContains(t, err, "cannot init while connected")
}

// TestPubSub_DisconnectIdempotent 断连幂等 + pool=nil 后 Disconnect 仍安全。
func TestPubSub_DisconnectIdempotent(t *testing.T) {
	mr := miniredis.RunT(t)
	bb := newTestBroker(t, mr)

	require.NoError(t, bb.Disconnect())
	assert.Nil(t, bb.pool)
	require.NoError(t, bb.Disconnect(), "pool=nil 后二次 Disconnect 应安全返回 nil")
}

// TestPubSub_UnsubscribeStopsDelivery 退订后发布不再投递，recv 循环退出。
func TestPubSub_UnsubscribeStopsDelivery(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroker(t, mr)

	hits := 0
	done := make(chan struct{})
	sub, err := b.Subscribe("topic.unsub", func(context.Context, broker.Event) error {
		hits++
		return nil
	}, nil)
	require.NoError(t, err)
	_ = sub

	time.Sleep(100 * time.Millisecond)
	require.NoError(t, sub.Unsubscribe(true))
	close(done)
	_ = done

	time.Sleep(100 * time.Millisecond)
	require.NoError(t, b.Publish(context.Background(), "topic.unsub", &broker.Message{Body: "late"}))
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 0, hits, "退订后发布不得投递")
}

// ---- 迭代 2：中间件链、Marshal 错误分支、诊断面访问器、重连退避 ----

func TestPubSub_MiddlewareChain(t *testing.T) {
	mr := miniredis.RunT(t)

	seenSub := 0
	b := newTestBroker(t, mr,
		broker.WithSubscriberMiddlewares(func(next broker.Handler) broker.Handler {
			return func(ctx context.Context, evt broker.Event) error {
				seenSub++
				return next(ctx, evt)
			}
		}),
		broker.WithPublishMiddlewares(func(next broker.PublishHandler) broker.PublishHandler {
			return func(ctx context.Context, topic string, msg *broker.Message, opts ...broker.PublishOption) error {
				assert.Equal(t, "topic.mw", topic, "发布中间件应拿到 topic")
				return next(ctx, topic, msg, opts...)
			}
		}),
	)

	got := make(chan []byte, 1)
	_, err := b.Subscribe("topic.mw", func(_ context.Context, evt broker.Event) error {
		got <- evt.Message().Body.([]byte)
		return nil
	}, nil)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, b.Publish(context.Background(), "topic.mw", &broker.Message{Body: "mw"}))

	select {
	case v := <-got:
		assert.Equal(t, []byte(`"mw"`), v)
	case <-time.After(3 * time.Second):
		t.Fatal("中间件链后订阅未收到消息")
	}
	assert.Equal(t, 1, seenSub, "订阅中间件应被驱动")
}

func TestPubSub_MarshalErrorBranch(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroker(t, mr)

	// channel/func 无法被 json codec 编码 → Marshal 失败分支。
	err := b.Publish(context.Background(), "t", &broker.Message{Body: make(chan int)})
	require.Error(t, err)
}

func TestPubSub_DiagnosticAccessors(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroker(t, mr)

	assert.Equal(t, "redis", b.Name())
	assert.NotNil(t, b.Options())

	sub, err := b.Subscribe("topic.acc", func(context.Context, broker.Event) error { return nil }, nil)
	require.NoError(t, err)
	assert.Equal(t, "topic.acc", sub.Topic())
	assert.NotNil(t, sub.Options())
}

// TestPubSub_ReconnectBackoffAfterServerClose miniredis 关停 → receiveLoop 报错 →
// reconnect 失败 → 退避重试 → Unsubscribe 终结循环（有界时长内完成）。
func TestPubSub_ReconnectBackoffAfterServerClose(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroker(t, mr)

	exited := make(chan struct{})
	sub, err := b.Subscribe("topic.dead", func(context.Context, broker.Event) error { return nil }, nil)
	require.NoError(t, err)
	watchSub := sub.(*subscriber)
	go func() {
		// recv 循环在错误后进入退避；closed 置位后下一次循环退出。
		time.Sleep(500 * time.Millisecond)
		_ = watchSub.Unsubscribe(true)
		close(exited)
	}()

	time.Sleep(200 * time.Millisecond)
	mr.Close() // 服务端消失 → 连接报错 → 退避重试路径

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("退订后 recv 循环未在退避窗口内退出")
	}
}
