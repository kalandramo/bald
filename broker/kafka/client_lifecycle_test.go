package kafka

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"testing"
	"time"

	kafkaGo "github.com/segmentio/kafka-go"

	"github.com/kalandramo/bald/broker"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mkMsg 构造测试用 kafka 消息。
func mkMsg(topic string, value []byte) kafkaGo.Message {
	return kafkaGo.Message{Topic: topic, Value: value}
}

// ---- WithScramMechanism 未知算法名 fail-fast（broker 原点守卫）----

func TestBroker_WithScramMechanismUnknownAlgoFailsFast(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "未知算法名必须构造期报错")
		msg, ok := r.(string)
		require.True(t, ok)
		assert.Contains(t, msg, `"sha256"`, "点名非法值本身")
		assert.Contains(t, msg, "SHA256", "并给出合法值域")
	}()

	_ = WithScramMechanism("sha256", "user", "pass") // 小写自然笔误 → 构造期炸
}

func TestBroker_WithScramMechanismValidAlgos(t *testing.T) {
	assert.NotPanics(t, func() {
		_ = WithScramMechanism("SHA256", "user", "pass")
		_ = WithScramMechanism("SHA512", "user", "pass")
	})
}

// ---- options 全表面：全部 Option 执行、落点可断言 ----

func TestBroker_OptionsAllFieldsApplied(t *testing.T) {
	mr := refusedAddr(t)
	completionCalled := false
	b := newTestBroker(t, mr,
		WithReaderConfig(kafkaGo.ReaderConfig{MaxWait: 50}), // 裸覆盖：Brokers 由 Addrs 贯通（见 newSubscriber）
		WithWriterConfig(WriterConfig{MaxAttempts: 1}),
		WithEnableOneTopicOneWriter(true),
		WithPlainMechanism("user", "pass"),
		WithScramMechanism("SHA256", "user", "pass"),
		WithMaxAttempts(2),
		WithLogger(kafkaGo.LoggerFunc(func(string, ...any) {})),
		WithErrorLogger(kafkaGo.LoggerFunc(func(string, ...any) {})),
		WithEnableLogger(false),
		WithEnableErrorLogger(false),
		WithBatchSize(3), // 发布侧 batch（writer 批）
		WithBatchTimeout(5),
		WithBatchBytes(1024),
		WithAsync(true),
		WithPublishMaxAttempts(1),
		WithReadTimeout(5),
		WithWriteTimeout(5),
		WithAllowPublishAutoTopicCreation(false),
		WithCompletion(func([]kafkaGo.Message, error) { completionCalled = true }),
	)
	require.NoError(t, b.Init())
	require.NoError(t, b.Connect())

	// 订阅侧 batch 选项经 SubscribeOption 贯通（subscribeBatchSizeKey）。
	sub, err := b.Subscribe("topic.opts", func(context.Context, broker.Event) error { return nil }, nil,
		WithSubscribeBatchSize(3),
		WithSubscribeBatchInterval(7*time.Second),
	)
	require.NoError(t, err)
	s := sub.(*subscriber)
	assert.Equal(t, 3, s.batchSize, "WithSubscribeBatchSize 应贯通订阅者")
	assert.Equal(t, 7*time.Second, s.batchInterval, "WithSubscribeBatchInterval 应贯通订阅者")
	_ = sub.Unsubscribe(true)
	_ = completionCalled
}

// ---- 订阅者消息处理链直驱（同包直达，无需真集群投递）----

func TestSubscriber_HandleMessageBranches(t *testing.T) {
	mr := refusedAddr(t)
	b := newTestBroker(t, mr)

	// 消息链分支全部 DisableAutoAck：AutoAck=true 的 CommitMessages 对拒连
	// 地址走 kafka-go 默认重试（10 次 × 超时）会挂死测试——commit 错误分支
	// 属真集群集成范围。
	newSub := func(binder broker.Binder) *subscriber {
		sub, err := b.Subscribe("topic.chain", func(context.Context, broker.Event) error { return nil }, binder)
		require.NoError(t, err)
		s := sub.(*subscriber)
		s.options.AutoAck = false
		return s
	}

	// binder nil：Body 落原始字节。
	s := newSub(nil)
	require.False(t, s.handleMessage(mkMsg("topic.chain", []byte("raw"))), "正常路径不应判 done")

	// binder + 合法 json：解码进容器。
	s2 := newSub(func() any { return new(map[string]any) })
	require.False(t, s2.handleMessage(mkMsg("topic.chain", []byte(`{"k":"v"}`))), "合法 json 不应判 done")

	// binder + 非法 json：unmarshal 失败 → done（跳过该消息）。
	s3 := newSub(func() any { return new(map[string]any) })
	require.True(t, s3.handleMessage(mkMsg("topic.chain", []byte("{invalid"))), "解码失败应判 done")

	// handler 出错：done（错误由 handler 决定语义）。
	s4 := newSub(nil)
	s4.handler = func(context.Context, broker.Event) error { return errors.New("handler boom") }
	require.True(t, s4.handleMessage(mkMsg("topic.chain", []byte("x"))), "handler 失败应判 done")
}

// ---- publication 访问器 ----

func TestPublication_Accessors(t *testing.T) {
	km := mkMsg("topic.acc", []byte("x"))
	bm := &broker.Message{Body: "x"}
	p := newPublication(context.Background(), nil, km, bm)

	assert.Equal(t, "topic.acc", p.Topic())
	assert.Same(t, bm, p.Message())
	assert.Equal(t, km, p.RawMessage())
	require.Error(t, p.Ack(), "reader=nil 时 Ack 报错（无集群不 commit）")

	p2 := &publication{err: assert.AnError}
	require.ErrorIs(t, p2.Error(), assert.AnError)
}

// ---- 诊断面访问器 ----

func TestBroker_DiagnosticAccessors(t *testing.T) {
	mr := refusedAddr(t)
	b := newTestBroker(t, mr)

	assert.Equal(t, "kafka", b.Name())
	assert.NotEmpty(t, b.Address(), "Init/Connect 后应有地址")
	assert.NotNil(t, b.Options())
}

// ---- 迭代 2：balancer 六分支直驱 initPublishOption、批量处理、诊断面 ----

func TestBroker_InitPublishOptionBalancers(t *testing.T) {
	cases := []struct {
		name    string
		opt     broker.PublishOption
		balType string
	}{
		{"least-bytes", WithLeastBytesBalancer(), "*kafka.LeastBytes"},
		{"round-robin", WithRoundRobinBalancer(), "*kafka.RoundRobin"},
		{"hash", WithHashBalancer(fnv.New32a()), "*kafka.Hash"},
		{"reference-hash", WithReferenceHashBalancer(fnv.New32a()), "*kafka.ReferenceHash"},
		{"crc32", WithCrc32Balancer(true), "*kafka.CRC32Balancer"},
		{"murmur2", WithMurmur2Balancer(true), "*kafka.Murmur2Balancer"},
	}
	for _, tc := range cases {
		w := &kafkaGo.Writer{}
		opts := broker.PublishOptions{Context: context.Background()}
		tc.opt(&opts)
		b := &kafkaBroker{}
		b.initPublishOption(w, opts)
		assert.Contains(t, fmt.Sprintf("%T", w.Balancer), tc.balType, tc.name)
	}

	// 未知 balancer（default 分支）：Balancer 保持 nil 不 panic。
	w := &kafkaGo.Writer{}
	opts := broker.PublishOptions{Context: context.Background()}
	b := &kafkaBroker{}
	b.initPublishOption(w, opts)
	assert.Nil(t, w.Balancer)
}

func TestBroker_PublishMiddlewareChain(t *testing.T) {
	mr := refusedAddr(t)
	seen := 0
	b := newTestBroker(t, mr,
		broker.WithPublishMiddlewares(func(next broker.PublishHandler) broker.PublishHandler {
			return func(ctx context.Context, topic string, msg *broker.Message, opts ...broker.PublishOption) error {
				seen++
				return next(ctx, topic, msg, opts...)
			}
		}),
	)
	require.NoError(t, b.Init())
	require.NoError(t, b.Connect())
	// 拒连 + 1 次尝试：错误快速返回，但中间件分支已驱动。
	require.Error(t, b.Publish(context.Background(), "topic.mw", &broker.Message{Body: "x"}))
	assert.Equal(t, 1, seen, "发布中间件应被驱动")
}

func TestSubscriber_HandleBatchMessage(t *testing.T) {
	mr := refusedAddr(t)
	b := newTestBroker(t, mr)

	hits := 0
	sub, err := b.Subscribe("topic.batch", func(context.Context, broker.Event) error { return nil }, nil)
	require.NoError(t, err)
	s := sub.(*subscriber)
	s.options.AutoAck = false // CommitMessages 对拒连地址无限重试会挂死测试
	s.handler = func(context.Context, broker.Event) error { hits++; return nil }

	msgs := []kafkaGo.Message{
		mkMsg("topic.batch", []byte("one")),
		mkMsg("topic.batch", []byte("two")),
	}
	s.handleBatchMessage(msgs)
	assert.Equal(t, 2, hits, "两条消息应全部驱动 handler")
}

func TestSubscriber_BinderFailureInBatch(t *testing.T) {
	mr := refusedAddr(t)
	b := newTestBroker(t, mr)

	sub, err := b.Subscribe("topic.batch.bad", func(context.Context, broker.Event) error { return nil }, func() any { return new(map[string]any) })
	require.NoError(t, err)
	s := sub.(*subscriber)
	s.options.AutoAck = false // 同上：commit 重试会挂死

	s.handleBatchMessage([]kafkaGo.Message{mkMsg("topic.batch.bad", []byte("{invalid"))}) // 解码失败 → done 记日志继续，不 panic
}

func TestSubscriber_DiagnosticAccessors(t *testing.T) {
	mr := refusedAddr(t)
	b := newTestBroker(t, mr)

	sub, err := b.Subscribe("topic.acc", func(context.Context, broker.Event) error { return nil }, nil)
	require.NoError(t, err)
	s := sub.(*subscriber)
	assert.Equal(t, "topic.acc", s.Topic())
	assert.NotNil(t, s.Options())
}

func TestBroker_AddressDefault(t *testing.T) {
	b := NewBroker()
	bb := b.(*kafkaBroker)
	// Addrs 未设置时 Address 落 defaultAddr（Init 才会写 options.Addrs）。
	assert.Equal(t, "127.0.0.1:9092", bb.Address())
}

func TestBroker_LoggerPrintf(t *testing.T) {
	Logger{}.Printf("unit %s", "driven")
	ErrorLogger{}.Printf("unit %s", "driven")
}

func TestBroker_ScramDialerWiring(t *testing.T) {
	mr := refusedAddr(t)
	b := newTestBroker(t, mr,
		WithScramMechanism("SHA256", "user", "pass"),
		WithReaderConfig(kafkaGo.ReaderConfig{Brokers: []string{mr}}), // 裸配置：Brokers 保留断言（缺陷 #8 回归）
	)
	require.NoError(t, b.Init())
	require.NoError(t, b.Connect())
	assert.Equal(t, []string{mr}, b.readerConfig.Brokers, "WithReaderConfig 替换不得抹掉 addrs 单一真相源")
}
