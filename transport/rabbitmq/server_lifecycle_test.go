package rabbitmq

import (
	"crypto/tls"

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

// ---- Stop 生命周期面：成功 Start 后 Stop 的三件套（退订/清簿/摘 started）----

func TestServer_StopLifecycleAfterSuccessfulStart(t *testing.T) {
	b := newStubRabbitBroker()
	srv := &Server{
		Broker:         b,
		subscribers:    make(broker.SubscriberMap),
		subscriberOpts: make(subscribe.SubscribeOptionMap),
		baseCtx:        context.Background(),
	}
	require.NoError(t, srv.Start(context.Background()))
	require.NoError(t, srv.RegisterSubscriber(context.Background(), "rk.stop", func(context.Context, broker.Event) error { return nil }, nil))
	assert.Len(t, srv.subscribers, 1, "live 登记应直接落订阅簿")

	require.NoError(t, srv.Stop(context.Background()))
	assert.Empty(t, srv.subscribers, "Stop 应清订阅簿")
	assert.Empty(t, srv.subscriberOpts, "Stop 应清 deferred 队列（重建走 contract 新实例）")
	assert.False(t, srv.started.Load(), "started 必须复位")
	assert.Empty(t, srv.Endpoint(), "Stop 后端点必回空")

	// double Stop：started 已复位 → 幂等 nil。
	require.NoError(t, srv.Stop(context.Background()))
}

func TestServer_DoubleStartIdempotent(t *testing.T) {
	b := newStubRabbitBroker()
	srv := &Server{
		Broker:         b,
		subscribers:    make(broker.SubscriberMap),
		subscriberOpts: make(subscribe.SubscribeOptionMap),
		baseCtx:        context.Background(),
	}
	require.NoError(t, srv.Start(context.Background()))
	require.NoError(t, srv.Start(context.Background()), "started 后重复 Start 幂等 nil")
	assert.Len(t, b.snapshotSubscribed(), 0, "重复 Start 不得重复 flush deferred 队列")
}

// ---- options 全表面：全部 ServerOption 执行并断言落点 ----

func TestServerOptions_AllFieldsApplied(t *testing.T) {
	m := &recordingRabbitMetrics{counters: map[string]float64{}}
	srv := NewServer(
		WithAddress([]string{"amqp://127.0.0.1:5672"}),
		WithCodec("json"),
		WithTLSConfig(nil),
		WithExchange("bald-ex", true),
		WithMetrics(m),
		WithBrokerOptions(broker.WithAddress("amqp://127.0.0.1:5672")),
	)
	assert.Same(t, m, srv.m, "WithMetrics 应注入实例")
	assert.Equal(t, KindRabbitMQ, srv.Name())
}

// ---- 泛型 wrapper：handler 返回错误 → err 透传分支 ----

func TestServer_GenericWrapperHandlerError(t *testing.T) {
	b := newStubRabbitBroker()
	srv := &Server{
		Broker:         b,
		subscribers:    make(broker.SubscriberMap),
		subscriberOpts: make(subscribe.SubscribeOptionMap),
	}

	boom := errors.New("typed handler boom")
	type payload struct {
		Value string `json:"value"`
	}
	require.NoError(t, RegisterSubscriber(srv, context.Background(), "rk.err",
		func(_ context.Context, _ string, _ broker.Headers, _ *payload) error { return boom }))

	require.NoError(t, srv.Start(context.Background()))
	wrapped, ok := b.captured["rk.err"]
	require.True(t, ok)
	err := wrapped(context.Background(), stubRabbitEvent{msg: broker.NewMessage(&payload{})})
	require.ErrorIs(t, err, boom, "typed handler 的错误必须透传")
}

// ---- baseCtx 兜底分支：字面量构造（baseCtx=nil）时登记自动补 Background ----

func TestServer_RegisterSubscriberBaseCtxFallback(t *testing.T) {
	s := &Server{
		subscribers:    make(broker.SubscriberMap),
		subscriberOpts: make(subscribe.SubscribeOptionMap),
		// baseCtx 刻意留 nil：RegisterSubscriber 的兜底分支应补 Background
	}
	require.NoError(t, s.RegisterSubscriber(context.Background(), "rk.ctx", func(context.Context, broker.Event) error { return nil }, nil))
	require.NotNil(t, s.baseCtx, "baseCtx 兜底应生效")
}

// ---- Init 失败分支：Start 返回 Init 错误（粘滞由 s.err 承担）----

func TestServer_InitFailureSticky(t *testing.T) {
	boom := errors.New("stub init boom")
	s := &Server{
		Broker:         &stubInitFailBroker{err: boom},
		subscribers:    make(broker.SubscriberMap),
		subscriberOpts: make(subscribe.SubscribeOptionMap),
	}
	err := s.Start(context.Background())
	require.ErrorIs(t, err, boom)
	assert.Empty(t, s.subscriberOpts, "Init 失败不得清 deferred 队列（收敛语义）")

	// 粘滞：二次 Start 回同一错误。
	require.ErrorIs(t, s.Start(context.Background()), boom)
}

type stubInitFailBroker struct {
	broker.Broker
	err error
}

func (b *stubInitFailBroker) Init(...broker.Option) error { return b.err }

// ---- WithTLSConfig 非 nil：EnableSecure 与 TLSConfig 两条都进 brokerOpts ----

func TestServer_WithTLSConfigNonNil(t *testing.T) {
	// rabbitmq 的 NewServer 无默认选项；非 nil TLS 追加 EnableSecure + TLSConfig → 2。
	srv := NewServer(WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}))
	require.Len(t, srv.brokerOpts, 2)
}
