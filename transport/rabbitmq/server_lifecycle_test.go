package rabbitmq

import (
	"context"
	"crypto/tls"
	"errors"
	"sync"
	"testing"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/encoding"
	jsoncodec "github.com/kalandramo/bald/encoding/json"
	"github.com/kalandramo/bald/transport/internal/brokertype"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 编码器显式注册（默认 codec 惰性查表——测试二进制里没人注册就查不到）。
func init() {
	encoding.MustRegister(jsoncodec.New())
}

// ---- 桩：行为见证用（生命周期/订阅簿语义的家族无关测试已上收 kernel 本包）----
// 与 kernel 桩同形，但经导出面驱动：换 Broker、读 captured/snapshot，
// 不直捅 kernel 未导出字段。

type stubBroker struct {
	broker.Broker
	mu          sync.Mutex
	subscribed  []string
	captured    map[string]broker.Handler
	capturedOpt map[string][]broker.SubscribeOption
}

func newStubBroker() *stubBroker {
	return &stubBroker{captured: map[string]broker.Handler{}, capturedOpt: map[string][]broker.SubscribeOption{}}
}

func (b *stubBroker) Init(...broker.Option) error { return nil }
func (b *stubBroker) Connect() error              { return nil }
func (b *stubBroker) Disconnect() error           { return nil }
func (b *stubBroker) Address() string             { return "amqp://stub:5672" }
func (b *stubBroker) Subscribe(routingKey string, handler broker.Handler, _ broker.Binder, opts ...broker.SubscribeOption) (broker.Subscriber, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribed = append(b.subscribed, routingKey)
	b.captured[routingKey] = handler
	b.capturedOpt[routingKey] = opts
	return &stubSubscriber{}, nil
}

func (b *stubBroker) snapshotSubscribed() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.subscribed...)
}

type stubSubscriber struct{}

func (s *stubSubscriber) Options() broker.SubscribeOptions { return broker.SubscribeOptions{} }
func (s *stubSubscriber) Topic() string                    { return "" }
func (s *stubSubscriber) Unsubscribe(bool) error           { return nil }

type stubEvent struct{ msg *broker.Message }

func (e stubEvent) Topic() string            { return "rk.gen" }
func (e stubEvent) Message() *broker.Message { return e.msg }
func (e stubEvent) RawMessage() any          { return nil }
func (e stubEvent) Ack() error               { return nil }
func (e stubEvent) Error() error             { return nil }

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

// ---- 泛型 wrapper 两分支：桩捕获直驱 ----

func TestServer_GenericWrapperBranches(t *testing.T) {
	b := newStubBroker()
	srv := NewServer()
	srv.Broker = b

	type payload struct {
		Value string `json:"value"`
	}
	typedHits := 0
	require.NoError(t, RegisterSubscriber(srv, context.Background(), "rk.gen",
		func(_ context.Context, _ string, _ broker.Headers, _ *payload) error { typedHits++; return nil }))

	require.NoError(t, srv.Start(context.Background())) // 桩 Connect 无网络
	defer func() { _ = srv.Stop(context.Background()) }()
	wrapped, ok := b.captured["rk.gen"]
	require.True(t, ok, "桩应捕获到包装后的 handler")

	// case *T 分支。
	require.NoError(t, wrapped(context.Background(), stubEvent{msg: broker.NewMessage(&payload{Value: "v"})}))
	assert.Equal(t, 1, typedHits, "typed handler 应被驱动一次")

	// default 分支：Body 非 *T → "unsupported type" 错误透传。
	err := wrapped(context.Background(), stubEvent{msg: broker.NewMessage("wrong-type")})
	require.ErrorContains(t, err, "unsupported type: string")
	assert.Equal(t, 1, typedHits, "default 分支不驱动 typed handler")
}

// recordingMetrics 本地计量桩（见证 WithMetrics 注入链；完整三路计数断言在
// kernel 本包的 WrapHandlerMetrics）。
type recordingMetrics struct {
	counters map[string]float64
}

func (m *recordingMetrics) Counter(_ context.Context, name string, value float64, _ map[string]string) {
	m.counters[name] += value
}

func (m *recordingMetrics) Histogram(context.Context, string, float64, map[string]string) {}
func (m *recordingMetrics) Gauge(context.Context, string, float64, map[string]string)     {}
func (m *recordingMetrics) GaugeAdd(context.Context, string, float64, map[string]string)  {}

// TestServer_MetricsOptionWired 经行为见证 WithMetrics 的注入链：
// 选项 → kernel 计量面 → wrapHandler 计数（桩捕获 wrapped handler 直驱）。
func TestServer_MetricsOptionWired(t *testing.T) {
	m := &recordingMetrics{counters: map[string]float64{}}
	srv := NewServer(WithMetrics(m))
	b := newStubBroker()
	srv.Broker = b

	require.NoError(t, srv.RegisterSubscriber(context.Background(), "rk.metrics", func(context.Context, broker.Event) error { return nil }, nil))
	require.NoError(t, srv.Start(context.Background()))
	defer func() { _ = srv.Stop(context.Background()) }()

	wrapped, ok := b.captured["rk.metrics"]
	require.True(t, ok, "桩应捕获到包装后的 handler")
	_ = wrapped(context.Background(), stubEvent{msg: broker.NewMessage("x")})
	assert.Positive(t, m.counters["broker_messages_received_total"], "received 计数必须亮（WithMetrics 注入链生效）")
}

// ---- 重入与幂等：Start 失败（started 未置真）后 double Stop 幂等 nil ----

func TestServer_DoubleStopIdempotent(t *testing.T) {
	srv := NewServer(WithAddress([]string{"amqp://127.0.0.1:1"}))
	require.Error(t, srv.Start(context.Background()))
	require.NoError(t, srv.Stop(context.Background()))
	require.NoError(t, srv.Stop(context.Background()))
	assert.True(t, hasPrefixRabbit(srv.Name(), "rabbit"))
}

func hasPrefixRabbit(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// ---- options 全表面：全部 ServerOption 执行 ----

func TestServerOptions_AllFieldsApplied(t *testing.T) {
	srv := NewServer(
		WithAddress([]string{"amqp://127.0.0.1:5672"}),
		WithCodec("json"),
		WithTLSConfig(nil),
		WithExchange("bald-ex", true),
		WithMetrics(&recordingMetrics{counters: map[string]float64{}}),
		WithBrokerOptions(broker.WithAddress("amqp://127.0.0.1:5672")),
	)
	assert.Equal(t, KindRabbitMQ, srv.Name())
}

// ---- 泛型 wrapper：handler 返回错误 → err 透传分支 ----

func TestServer_GenericWrapperHandlerError(t *testing.T) {
	b := newStubBroker()
	srv := NewServer()
	srv.Broker = b

	boom := errors.New("typed handler boom")
	type payload struct {
		Value string `json:"value"`
	}
	require.NoError(t, RegisterSubscriber(srv, context.Background(), "rk.err",
		func(_ context.Context, _ string, _ broker.Headers, _ *payload) error { return boom }))

	require.NoError(t, srv.Start(context.Background()))
	defer func() { _ = srv.Stop(context.Background()) }()
	wrapped, ok := b.captured["rk.err"]
	require.True(t, ok)
	err := wrapped(context.Background(), stubEvent{msg: broker.NewMessage(&payload{})})
	require.ErrorIs(t, err, boom, "typed handler 的错误必须透传")
}

// ---- WithTLSConfig 非 nil：EnableSecure 与 TLSConfig 两条都进 broker 构造面
// （经捕参家族见证——brokerOpts 收集面在 kernel，计数看 NewBroker 收到的实参）----

func TestServer_WithTLSConfigNonNil(t *testing.T) {
	fam := Family()
	var captured []broker.Option
	fam.NewBroker = func(opts ...broker.Option) broker.Broker {
		captured = opts
		return newStubBroker()
	}
	s := &Server{Server: brokertype.NewServer(fam)}
	WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12})(s)
	s.BuildBroker()
	// rabbitmq 的 NewServer 无默认选项；非 nil TLS 追加 EnableSecure + TLSConfig → 2。
	require.Len(t, captured, 2)
}
