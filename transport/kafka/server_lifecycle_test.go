package kafka

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/encoding"
	jsoncodec "github.com/kalandramo/bald/encoding/json"
	"github.com/kalandramo/bald/transport/subscribe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 编码器显式注册（默认 codec 惰性查表——测试二进制里没人注册就查不到）。
func init() {
	encoding.MustRegister(jsoncodec.New())
}

// ---- 桩：offline 验证 doRegisterSubscriberMap 的收敛语义与覆盖摘除 ----

type stubKafkaBroker struct {
	broker.Broker // 只被 Subscribe 走到，其余方法不触碰
	failTopics    map[string]bool
	initErr       error
	connectErr    error

	mu         sync.Mutex
	subscribed []string
}

func (b *stubKafkaBroker) Init(...broker.Option) error { return b.initErr }
func (b *stubKafkaBroker) Connect() error              { return b.connectErr }
func (b *stubKafkaBroker) Subscribe(topic string, _ broker.Handler, _ broker.Binder, _ ...broker.SubscribeOption) (broker.Subscriber, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failTopics[topic] {
		return nil, fmt.Errorf("stub: subscribe %s forced failure", topic)
	}
	b.subscribed = append(b.subscribed, topic)
	return &stubKafkaSubscriber{}, nil
}

func (b *stubKafkaBroker) snapshotSubscribed() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.subscribed...)
}

type stubKafkaSubscriber struct {
	unsubscribed bool
}

func (s *stubKafkaSubscriber) Options() broker.SubscribeOptions { return broker.SubscribeOptions{} }
func (s *stubKafkaSubscriber) Topic() string                    { return "" }
func (s *stubKafkaSubscriber) Unsubscribe(bool) error {
	s.unsubscribed = true
	return nil
}

// TestServer_PartialRegistrationFailureKeepsDeferred 锁定收敛语义（段2 假设1 的 kafka 平移）：
// doRegisterSubscriberMap 部分失败时，失败条目留在 subscriberOpts 供下次 Start 重试，
// 成功条目才摘除——旧实现无条件清空整批 deferred 队列（家族同构缺陷）。
func TestServer_PartialRegistrationFailureKeepsDeferred(t *testing.T) {
	b := &stubKafkaBroker{failTopics: map[string]bool{"bad": true}}
	s := &Server{
		Broker:         b,
		subscribers:    make(broker.SubscriberMap),
		subscriberOpts: make(subscribe.SubscribeOptionMap),
	}
	s.subscriberOpts["good"] = &subscribe.SubscribeOption{Handler: func(context.Context, broker.Event) error { return nil }}
	s.subscriberOpts["bad"] = &subscribe.SubscribeOption{Handler: func(context.Context, broker.Event) error { return nil }}

	err := s.doRegisterSubscriberMap()
	require.Error(t, err, "部分失败应上抛")
	assert.Contains(t, err.Error(), "bad")

	if _, ok := s.subscriberOpts["good"]; ok {
		t.Fatal("成功条目应从 deferred 队列摘除")
	}
	require.Contains(t, s.subscriberOpts, "bad", "失败条目必须保留待重试")
	assert.Contains(t, b.snapshotSubscribed(), "good")
	assert.NotContains(t, b.snapshotSubscribed(), "bad")

	// 修复缺陷后重试：bad 收敛补齐。
	b2 := &stubKafkaBroker{}
	s.Broker = b2
	require.NoError(t, s.doRegisterSubscriberMap())
	assert.Contains(t, b2.snapshotSubscribed(), "bad")
	if _, ok := s.subscriberOpts["bad"]; ok {
		t.Fatal("重试成功后 deferred 队列应清空")
	}
}

// TestServer_RegisterDuplicateOverwriteUnsubscribesOld 锁定覆盖语义（段2 假设3 的 kafka 平移）：
// 同 topic 覆盖必须先摘旧订阅——只覆写映射的话消息双份投递且订阅 goroutine 泄漏。
func TestServer_RegisterDuplicateOverwriteUnsubscribesOld(t *testing.T) {
	b := &stubKafkaBroker{}
	s := &Server{
		Broker:         b,
		subscribers:    make(broker.SubscriberMap),
		subscriberOpts: make(subscribe.SubscribeOptionMap),
	}

	old := &stubKafkaSubscriber{}
	s.subscribers["topic.dup"] = old

	require.NoError(t, s.doRegisterSubscriber("topic.dup", func(context.Context, broker.Event) error { return nil }, nil))
	assert.True(t, old.unsubscribed, "覆盖前必须摘除旧订阅")
	assert.Len(t, s.subscribers, 1, "仍只有一个订阅者条目")
}

// ---- 离线生命周期：kafka 的 Connect 只翻标志位，无集群 Start 也绿 ----

func TestServer_OfflineLifecycle(t *testing.T) {
	srv := NewServer() // 不配地址：Connect 翻标志位，离线绿

	require.NoError(t, srv.Start(context.Background()))
	assert.True(t, srv.started.Load())
	require.NotEmpty(t, srv.Endpoint())
	assert.True(t, stringsHasPrefix(srv.Endpoint(), "kafka://"), "端点应带 scheme: %q", srv.Endpoint())

	// deferred 登记（Start 后走直连路径）。
	require.NoError(t, srv.RegisterSubscriber(context.Background(), "topic.live", "g1", false, func(context.Context, broker.Event) error { return nil }, nil))
	assert.Len(t, srv.subscribers, 1, "Start 后登记应立即落订阅簿")

	require.NoError(t, srv.Stop(context.Background()))
	assert.Empty(t, srv.Endpoint(), "Stop 后端点必回空")
	assert.Empty(t, srv.subscribers)
	assert.Empty(t, srv.subscriberOpts, "Stop 清 deferred 是有意语义：重建走 contract 新实例")

	require.NoError(t, srv.Stop(context.Background()), "重复 Stop 幂等")
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// ---- 失败粘滞：Init/Connect 失败后 s.err 粘滞，二次 Start 回同一错误 ----

func TestServer_StartFailureSticky(t *testing.T) {
	boom := errors.New("stub connect boom")
	s := &Server{
		Broker:         &stubKafkaBroker{connectErr: boom},
		subscribers:    make(broker.SubscriberMap),
		subscriberOpts: make(subscribe.SubscribeOptionMap),
	}

	err := s.Start(context.Background())
	require.ErrorIs(t, err, boom)

	err2 := s.Start(context.Background())
	require.Equal(t, err.Error(), err2.Error(), "粘滞错误应一致")
}

// ---- options 全表面：全部 ServerOption 执行且离线 Start 绿 ----

func TestServerOptions_AllFieldsApplied(t *testing.T) {
	m := &recordingKafkaMetrics{counters: map[string]float64{}}
	srv := NewServer(
		WithAddress([]string{"127.0.0.1:9092"}),
		WithCodec("json"),
		WithTLSConfig(nil),
		WithPlainMechanism("user", "pass"),
		WithScramMechanism("SHA256", "user", "pass"),
		WithMetrics(m),
		WithBrokerOptions(broker.WithAddress("127.0.0.1:9092")),
	)
	assert.Same(t, m, srv.m, "WithMetrics 应注入实例")

	require.NoError(t, srv.Start(context.Background()), "全选项构造后离线 Start 应绿")
	defer func() { _ = srv.Stop(context.Background()) }()
}

// ---- wrapHandler：metrics 注入后的三路计数（直接单元驱动，无需真集群投递） ----

type recordingKafkaMetrics struct {
	counters map[string]float64
	histos   []string
	errored  bool
}

func (m *recordingKafkaMetrics) Counter(_ context.Context, name string, value float64, _ map[string]string) {
	m.counters[name] += value
	if name == "broker_messages_errors_total" {
		m.errored = true
	}
}

func (m *recordingKafkaMetrics) Histogram(_ context.Context, name string, _ float64, _ map[string]string) {
	m.histos = append(m.histos, name)
}

func (m *recordingKafkaMetrics) Gauge(context.Context, string, float64, map[string]string)    {}
func (m *recordingKafkaMetrics) GaugeAdd(context.Context, string, float64, map[string]string) {}

type stubKafkaEvent struct{}

func (stubKafkaEvent) Topic() string            { return "topic.m" }
func (stubKafkaEvent) Message() *broker.Message { return broker.NewMessage("x") }
func (stubKafkaEvent) RawMessage() any          { return nil }
func (stubKafkaEvent) Ack() error               { return nil }
func (stubKafkaEvent) Error() error             { return nil }

func TestServer_WrapHandlerMetrics(t *testing.T) {
	m := &recordingKafkaMetrics{counters: map[string]float64{}}
	s := &Server{m: m}

	handlerErr := errors.New("handler boom")
	wrapped := s.wrapHandler("topic.m", func(context.Context, broker.Event) error {
		return handlerErr
	})

	err := wrapped(context.Background(), stubKafkaEvent{})
	require.ErrorIs(t, err, handlerErr, "handler 错误应透传")
	assert.Positive(t, m.counters["broker_messages_received_total"], "received 计数必须亮")
	assert.NotEmpty(t, m.histos, "duration 直方图必须记录")
	assert.True(t, m.errored, "handler 错误必须进 error 计数")
	assert.Equal(t, KindKafka, s.Name())
}

// ---- WithScramMechanism 未知算法名 fail-fast（构造期点名非法值，不透传给
// scram 库深处 panic）----

func TestServer_WithScramMechanismUnknownAlgoFailsFast(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "未知算法名必须构造期报错")
		msg, ok := r.(string)
		require.True(t, ok, "panic 值应为 string")
		assert.Contains(t, msg, `"sha256"`, "点名非法值本身")
		assert.Contains(t, msg, "SHA256", "并给出合法值域")
	}()

	_ = WithScramMechanism("sha256", "user", "pass") // 小写自然笔误 → 构造期炸
}

func TestServer_WithScramMechanismValidAlgosConstruct(t *testing.T) {
	assert.NotPanics(t, func() {
		_ = WithScramMechanism("SHA256", "user", "pass")
		_ = WithScramMechanism("SHA512", "user", "pass")
	})
}
