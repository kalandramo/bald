package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

type stubRabbitBroker struct {
	broker.Broker // 只被 Subscribe 走到，其余方法不触碰
	failTopics    map[string]bool

	mu         sync.Mutex
	subscribed []string
	captured   map[string]broker.Handler // routingKey → wrapHandler 包装后的 handler
}

func newStubRabbitBroker() *stubRabbitBroker {
	return &stubRabbitBroker{
		failTopics: map[string]bool{},
		captured:   map[string]broker.Handler{},
	}
}

func (b *stubRabbitBroker) Init(...broker.Option) error { return nil }
func (b *stubRabbitBroker) Connect() error              { return nil }
func (b *stubRabbitBroker) Disconnect() error           { return nil }
func (b *stubRabbitBroker) Address() string             { return "amqp://stub:5672" }

func (b *stubRabbitBroker) Subscribe(routingKey string, handler broker.Handler, _ broker.Binder, _ ...broker.SubscribeOption) (broker.Subscriber, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failTopics[routingKey] {
		return nil, fmt.Errorf("stub: subscribe %s forced failure", routingKey)
	}
	b.subscribed = append(b.subscribed, routingKey)
	b.captured[routingKey] = handler
	return &stubRabbitSubscriber{}, nil
}

func (b *stubRabbitBroker) snapshotSubscribed() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.subscribed...)
}

type stubRabbitSubscriber struct {
	unsubscribed bool
}

func (s *stubRabbitSubscriber) Options() broker.SubscribeOptions { return broker.SubscribeOptions{} }
func (s *stubRabbitSubscriber) Topic() string                    { return "" }
func (s *stubRabbitSubscriber) Unsubscribe(bool) error {
	s.unsubscribed = true
	return nil
}

// TestServer_PartialRegistrationFailureKeepsDeferred 锁定收敛语义（段2 假设1 的 rabbitmq 平移）：
// 部分失败时失败条目留在 subscriberOpts 供下次 Start 重试，成功条目才摘除。
func TestServer_PartialRegistrationFailureKeepsDeferred(t *testing.T) {
	b := newStubRabbitBroker()
	b.failTopics["bad"] = true
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

	// 重试收敛：bad 补齐。
	b2 := newStubRabbitBroker()
	s.Broker = b2
	require.NoError(t, s.doRegisterSubscriberMap())
	assert.Contains(t, b2.snapshotSubscribed(), "bad")
	if _, ok := s.subscriberOpts["bad"]; ok {
		t.Fatal("重试成功后 deferred 队列应清空")
	}
}

// TestServer_RegisterDuplicateOverwriteUnsubscribesOld 锁定覆盖语义（段2 假设3 的 rabbitmq 平移）：
// 同 routingKey 覆盖必须先摘旧订阅——否则消息双份投递且订阅 goroutine 泄漏。
func TestServer_RegisterDuplicateOverwriteUnsubscribesOld(t *testing.T) {
	b := newStubRabbitBroker()
	s := &Server{
		Broker:         b,
		subscribers:    make(broker.SubscriberMap),
		subscriberOpts: make(subscribe.SubscribeOptionMap),
	}

	old := &stubRabbitSubscriber{}
	s.subscribers["rk.dup"] = old

	require.NoError(t, s.doRegisterSubscriber("rk.dup", func(context.Context, broker.Event) error { return nil }, nil))
	assert.True(t, old.unsubscribed, "覆盖前必须摘除旧订阅")
	assert.Len(t, s.subscribers, 1, "仍只有一个订阅者条目")
}

// ---- 真拨号失败路径：拒连端口 → Start 粘滞失败、端点保持空 ----

func TestServer_RefusedPortSticky(t *testing.T) {
	// AMQP 真拨号（进程内假握手不可信）——拒连端口是离线可判的失败形状。
	srv := NewServer(WithAddress([]string{"amqp://127.0.0.1:1"}))

	err := srv.Start(context.Background())
	require.Error(t, err, "拒连地址 Start 必失败")

	err2 := srv.Start(context.Background())
	require.Equal(t, err.Error(), err2.Error(), "粘滞错误应一致")
	assert.Empty(t, srv.Endpoint(), "Start 失败时端点保持空（started 未置真）")
}

// ---- nil ctx 守卫（家族里 rabbitmq 独有的防御）----

func TestServer_RegisterSubscriberNilCtxGuard(t *testing.T) {
	srv := NewServer()
	// ctx=nil 不得 panic：回落 baseCtx 后正常入队。
	require.NoError(t, srv.RegisterSubscriber(nil, "rk.nil", func(context.Context, broker.Event) error { return nil }, nil))
	require.Contains(t, srv.subscriberOpts, "rk.nil")
}

// ---- 泛型 wrapper 两分支 + wrapHandler 计量：桩捕获直驱 ----

type stubRabbitEvent struct{ msg *broker.Message }

func (e stubRabbitEvent) Topic() string            { return "rk.gen" }
func (e stubRabbitEvent) Message() *broker.Message { return e.msg }
func (e stubRabbitEvent) RawMessage() any          { return nil }
func (e stubRabbitEvent) Ack() error               { return nil }
func (e stubRabbitEvent) Error() error             { return nil }

func TestServer_GenericWrapperBranches(t *testing.T) {
	b := newStubRabbitBroker()
	srv := &Server{
		Broker:         b,
		subscribers:    make(broker.SubscriberMap),
		subscriberOpts: make(subscribe.SubscribeOptionMap),
	}

	type payload struct {
		Value string `json:"value"`
	}
	typedHits := 0
	require.NoError(t, RegisterSubscriber(srv, context.Background(), "rk.gen",
		func(_ context.Context, _ string, _ broker.Headers, _ *payload) error { return nil }))

	require.NoError(t, srv.Start(context.Background())) // 桩 Connect 无网络
	wrapped, ok := b.captured["rk.gen"]
	require.True(t, ok, "桩应捕获到包装后的 handler")

	// case *T 分支。
	require.NoError(t, wrapped(context.Background(), stubRabbitEvent{msg: broker.NewMessage(&payload{Value: "v"})}))

	// default 分支：Body 非 *T → "unsupported type" 错误透传。
	err := wrapped(context.Background(), stubRabbitEvent{msg: broker.NewMessage("wrong-type")})
	require.ErrorContains(t, err, "unsupported type: string")

	_ = typedHits
}

type recordingRabbitMetrics struct {
	counters map[string]float64
	histos   []string
	errored  bool
}

func (m *recordingRabbitMetrics) Counter(_ context.Context, name string, value float64, _ map[string]string) {
	m.counters[name] += value
	if name == "broker_messages_errors_total" {
		m.errored = true
	}
}

func (m *recordingRabbitMetrics) Histogram(_ context.Context, name string, _ float64, _ map[string]string) {
	m.histos = append(m.histos, name)
}

func (m *recordingRabbitMetrics) Gauge(context.Context, string, float64, map[string]string)    {}
func (m *recordingRabbitMetrics) GaugeAdd(context.Context, string, float64, map[string]string) {}

func TestServer_WrapHandlerMetrics(t *testing.T) {
	m := &recordingRabbitMetrics{counters: map[string]float64{}}
	s := &Server{m: m}

	handlerErr := errors.New("handler boom")
	wrapped := s.wrapHandler("rk.metrics", func(context.Context, broker.Event) error {
		return handlerErr
	})

	err := wrapped(context.Background(), stubRabbitEvent{msg: broker.NewMessage("x")})
	require.ErrorIs(t, err, handlerErr, "handler 错误应透传")
	assert.Positive(t, m.counters["broker_messages_received_total"], "received 计数必须亮")
	assert.NotEmpty(t, m.histos, "duration 直方图必须记录")
	assert.True(t, m.errored, "handler 错误必须进 error 计数")
	assert.Equal(t, KindRabbitMQ, s.Name())
}

// ---- 重入与幂等：double Start（拒连粘滞下二次失败）/ double Stop ----

func TestServer_DoubleStopIdempotent(t *testing.T) {
	srv := NewServer(WithAddress([]string{"amqp://127.0.0.1:1"}))
	require.Error(t, srv.Start(context.Background()))
	// Start 失败（started 未置真）→ Stop 幂等 nil。
	require.NoError(t, srv.Stop(context.Background()))
	require.NoError(t, srv.Stop(context.Background()))
	assert.True(t, strings.Contains(srv.Name(), "rabbit"))
}
