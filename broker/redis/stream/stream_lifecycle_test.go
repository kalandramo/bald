package stream

import (
	"context"
	"testing"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/kalandramo/bald/broker"
	redisOption "github.com/kalandramo/bald/broker/redis/option"
	"github.com/kalandramo/bald/encoding"
	jsoncodec "github.com/kalandramo/bald/encoding/json"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestBroker 构造已 Init+Connect 的 stream broker（miniredis 后端）。
// 编码器显式注册（默认 codec 惰性查表——测试二进制里没人注册就查不到）。
func init() {
	encoding.MustRegister(jsoncodec.New())
}

func newTestBroker(t *testing.T, mr *miniredis.Miniredis, opts ...broker.Option) *streamBroker {
	t.Helper()
	bb, ok := NewBroker(append([]broker.Option{broker.WithAddress(mr.Addr())}, opts...)...).(*streamBroker)
	require.True(t, ok, "NewBroker 应返回具体类型")
	require.NoError(t, bb.Init())
	require.NoError(t, bb.Connect())
	t.Cleanup(func() { _ = bb.Disconnect() })
	return bb
}

// TestStream_PublishSubscribeRoundtrip 消费组回环：Subscribe（XGROUP CREATE $）
// → Publish（XADD）→ XREADGROUP 消费 → handler 收到 body。
func TestStream_PublishSubscribeRoundtrip(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroker(t, mr)

	type payload struct {
		Value string `json:"value"`
	}
	got := make(chan string, 1)
	_, err := b.Subscribe("topic.stream", func(_ context.Context, evt broker.Event) error {
		p, ok := evt.Message().Body.(*payload)
		if !ok {
			t.Errorf("body type = %T, want *payload", evt.Message().Body)
			return nil
		}
		got <- p.Value
		return nil
	}, func() any { return new(payload) })
	require.NoError(t, err)

	// 等消费组与 XREADGROUP 循环就位。
	time.Sleep(200 * time.Millisecond)
	require.NoError(t, b.Publish(context.Background(), "topic.stream", &broker.Message{Body: payload{Value: "streamed"}}))

	select {
	case v := <-got:
		assert.Equal(t, "streamed", v)
	case <-time.After(5 * time.Second):
		t.Fatal("stream 订阅未收到消息")
	}
}

// TestStream_AckSemantics AutoAck 语义：默认开（投递后 XACK 清 pending），
// DisableAutoAck 后消息滞留 pending，手动 Ack 才清。
func TestStream_AckSemantics(t *testing.T) {
	mr := miniredis.RunT(t)

	// 默认 AutoAck：投递即确认。
	b := newTestBroker(t, mr)
	ackDone := make(chan struct{}, 1)
	_, err := b.Subscribe("topic.ack", func(context.Context, broker.Event) error { return nil }, nil)
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)
	require.NoError(t, b.Publish(context.Background(), "topic.ack", &broker.Message{Body: "a"}))
	waitPendingZero(t, mr, "topic.ack", redisOption.DefaultStreamGroup, "AutoAck 投递后 pending 应清零")
	close(ackDone)
	_ = ackDone

	// DisableAutoAck：消息滞留 pending；手动 p.Ack() 才清。
	b2 := newTestBroker(t, mr)
	type payload struct {
		Value string `json:"value"`
	}
	manualAck := make(chan interface{ Ack() error }, 1)
	_, err = b2.Subscribe("topic.nack",
		func(_ context.Context, evt broker.Event) error {
			manualAck <- evt.(interface{ Ack() error })
			return nil
		},
		func() any { return new(payload) },
		broker.DisableAutoAck())
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)
	require.NoError(t, b2.Publish(context.Background(), "topic.nack", &broker.Message{Body: payload{Value: "n"}}))

	var pub interface{ Ack() error }
	select {
	case pub = <-manualAck:
	case <-time.After(5 * time.Second):
		t.Fatal("DisableAutoAck 订阅未收到消息")
	}
	waitPending(t, mr, "topic.nack", redisOption.DefaultStreamGroup, 1, "DisableAutoAck 下消息应滞留 pending")
	require.NoError(t, pub.Ack())
	waitPendingZero(t, mr, "topic.nack", redisOption.DefaultStreamGroup, "手动 Ack 后 pending 清零")
}

// waitPendingZero 轮询等待 pending 归零（ack 是异步落账）。
func waitPendingZero(t *testing.T, mr *miniredis.Miniredis, stream, group, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pendingOf(t, mr, stream, group) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	c, derr := redis.DialURL("redis://" + mr.Addr())
	if derr == nil {
		raw, _ := c.Do("XPENDING", stream, group)
		t.Logf("DEBUG XPENDING raw=%v", raw)
		_ = c.Close()
	}
	t.Fatalf("%s: pending 未归零", msg)
}

// waitPending 轮询等待 pending 到达期望值。
func waitPending(t *testing.T, mr *miniredis.Miniredis, stream, group string, want int, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pendingOf(t, mr, stream, group) == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: pending=%d, want %d", msg, pendingOf(t, mr, stream, group), want)
}

// pendingOf 裸命令 XPENDING 取首元素计数。
func pendingOf(t *testing.T, mr *miniredis.Miniredis, stream, group string) int {
	t.Helper()
	c, err := redis.DialURL("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("dial miniredis: %v", err)
	}
	defer func() { _ = c.Close() }()
	reply, err := redis.Values(c.Do("XPENDING", stream, group))
	if err != nil || len(reply) == 0 {
		return 0
	}
	n, _ := redis.Int(reply[0], nil)
	return n
}

// TestStream_NotConnectedGuards 未 Connect 时 Publish/Subscribe 前置防护（不 panic）。
func TestStream_NotConnectedGuards(t *testing.T) {
	b := NewBroker()
	require.NoError(t, b.Init())

	err := b.Publish(context.Background(), "t", &broker.Message{Body: "x"})
	require.ErrorContains(t, err, "not connected")

	_, err = b.Subscribe("t", func(context.Context, broker.Event) error { return nil }, nil)
	require.ErrorContains(t, err, "not connected")
}

// TestStream_ConnectRefusedProbe 拒连端口：Connect 探活暴露 + pool 丢弃。
func TestStream_ConnectRefusedProbe(t *testing.T) {
	b := NewBroker()
	require.NoError(t, b.Init())
	bb := b.(*streamBroker)
	bb.options.Addrs = []string{"127.0.0.1:1"}

	require.Error(t, bb.Connect(), "PING 探针必须暴露拒连")
	assert.Nil(t, bb.pool)
}

// TestStream_EnsureGroupBusyGroupTolerated 二次 Subscribe 同 group：BUSYGROUP 容忍。
func TestStream_EnsureGroupBusyGroupTolerated(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroker(t, mr)

	for i := 0; i < 2; i++ {
		_, err := b.Subscribe("topic.busy", func(context.Context, broker.Event) error { return nil }, nil)
		require.NoError(t, err, "第 %d 次 Subscribe：BUSYGROUP 应被容忍", i+1)
		time.Sleep(100 * time.Millisecond)
	}
}

// TestStream_MaxLenPublish WithStreamMaxLen 下 XADD 带 MAXLEN 仍成功。
func TestStream_MaxLenPublish(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroker(t, mr)

	require.NoError(t, b.Publish(context.Background(), "topic.maxlen",
		&broker.Message{Body: "x"}, redisOption.WithStreamMaxLen(100)))
}

// TestStream_SubscribeOptionsPassthrough 流专属 option 落 subscriber 字段。
func TestStream_SubscribeOptionsPassthrough(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroker(t, mr)

	sub, err := b.Subscribe("topic.opts", func(context.Context, broker.Event) error { return nil }, nil,
		redisOption.WithStreamGroup("g-custom"),
		redisOption.WithStreamConsumer("c-custom"),
		redisOption.WithStreamBlockTime(250*time.Millisecond),
		redisOption.WithStreamCount(5),
	)
	require.NoError(t, err)
	require.NotNil(t, sub)

	s := sub.(*subscriber)
	assert.Equal(t, "g-custom", s.group)
	assert.Equal(t, "c-custom", s.consumer)
	assert.Equal(t, 250*time.Millisecond, s.blockTime)
	assert.Equal(t, 5, s.count)
}

// TestStream_ExtractField 提取器：命中/未命中两分支。
func TestStream_ExtractField(t *testing.T) {
	s := &subscriber{}
	fields := []any{[]byte("body"), []byte("v"), []byte("other"), []byte("w")}

	assert.Equal(t, []byte("v"), s.extractField(fields, "body"))
	assert.Nil(t, s.extractField(fields, "missing"))
}

// TestStream_UnsubscribeStopsDelivery 退订后消息不再投递（receiveLoop 收敛退出）。
func TestStream_UnsubscribeStopsDelivery(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroker(t, mr)

	hits := 0
	sub, err := b.Subscribe("topic.unsub", func(context.Context, broker.Event) error {
		hits++
		return nil
	}, nil)
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)
	require.NoError(t, sub.Unsubscribe(true))

	time.Sleep(100 * time.Millisecond)
	require.NoError(t, b.Publish(context.Background(), "topic.unsub", &broker.Message{Body: "late"}))
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 0, hits, "退订后发布不得投递")
}

// ---- 迭代 2：诊断面访问器、Ack 边界、Init 选项贯通、发布中间件 ----

func TestStream_DiagnosticAccessors(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroker(t, mr)

	assert.Equal(t, "redis-stream", b.Name())
	assert.NotNil(t, b.Options())
	assert.Equal(t, "redis://"+mr.Addr(), b.Address())

	sub, err := b.Subscribe("topic.acc", func(context.Context, broker.Event) error { return nil }, nil)
	require.NoError(t, err)
	assert.Equal(t, "topic.acc", sub.Topic())
	assert.NotNil(t, sub.Options())
}

func TestStream_PublicationAckBranches(t *testing.T) {
	// 已 acked → 幂等 nil；pool nil → 无处 ack 返回 nil。
	p := &publication{acked: true}
	require.NoError(t, p.Ack())

	p2 := &publication{pool: nil}
	require.NoError(t, p2.Ack())

	// Error 访问器透传。
	p3 := &publication{err: assert.AnError, message: &broker.Message{}}
	require.ErrorIs(t, p3.Error(), assert.AnError)
	assert.NotNil(t, p3.Message())
	assert.NotNil(t, p3.RawMessage())
}

func TestStream_InitOptionPassthrough(t *testing.T) {
	b := NewBroker(redisOption.WithMaxIdle(3), redisOption.WithMaxActive(7))
	bb := b.(*streamBroker)
	require.NoError(t, bb.Init())
	// Init(opts...) 应用后 commonOpts 从 Context 取回贯通值。
	assert.Equal(t, 3, bb.commonOpts.MaxIdle)
	assert.Equal(t, 7, bb.commonOpts.MaxActive)
}

func TestStream_MarshalErrorBranch(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroker(t, mr)

	err := b.Publish(context.Background(), "t", &broker.Message{Body: make(chan int)})
	require.Error(t, err)
}

func TestStream_PublishMiddlewareChain(t *testing.T) {
	mr := miniredis.RunT(t)
	seen := 0
	b := newTestBroker(t, mr,
		broker.WithPublishMiddlewares(func(next broker.PublishHandler) broker.PublishHandler {
			return func(ctx context.Context, topic string, msg *broker.Message, opts ...broker.PublishOption) error {
				seen++
				return next(ctx, topic, msg, opts...)
			}
		}),
	)
	require.NoError(t, b.Publish(context.Background(), "topic.mw", &broker.Message{Body: "x"}))
	assert.Equal(t, 1, seen, "发布中间件应被驱动")
}
