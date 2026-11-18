package kafka

import (
	"context"
	"crypto/tls"
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
	capturedOpt map[string][]broker.SubscribeOption // key → Subscribe 时收到的选项
}

func newStubBroker() *stubBroker {
	return &stubBroker{captured: map[string]broker.Handler{}, capturedOpt: map[string][]broker.SubscribeOption{}}
}

func (b *stubBroker) Init(...broker.Option) error { return nil }
func (b *stubBroker) Connect() error              { return nil }
func (b *stubBroker) Disconnect() error           { return nil }
func (b *stubBroker) Address() string             { return "stub:9092" }
func (b *stubBroker) Subscribe(topic string, handler broker.Handler, _ broker.Binder, opts ...broker.SubscribeOption) (broker.Subscriber, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribed = append(b.subscribed, topic)
	b.captured[topic] = handler
	b.capturedOpt[topic] = opts
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

func (e stubEvent) Topic() string            { return "topic.m" }
func (e stubEvent) Message() *broker.Message { return e.msg }
func (e stubEvent) RawMessage() any          { return nil }
func (e stubEvent) Ack() error               { return nil }
func (e stubEvent) Error() error             { return nil }

// ---- 离线生命周期：kafka 的 Connect 只翻标志位，无集群 Start 也绿 ----
// started/订阅簿的字段面 witnesses 改走公共观测点：Endpoint 非空 ⟺ started，
// stub 的 snapshot ⟺ 订阅簿，stub 收到 Unsubscribe ⟺ 退订发生。

func TestServer_OfflineLifecycle(t *testing.T) {
	srv := NewServer() // 不配地址：Connect 翻标志位，离线绿

	require.NoError(t, srv.Start(context.Background()))
	require.NotEmpty(t, srv.Endpoint())
	assert.True(t, hasPrefix(srv.Endpoint(), "kafka://"), "端点应带 scheme: %q", srv.Endpoint())

	// deferred 登记（Start 后走直连路径）。
	b := newStubBroker()
	srv.Broker = b
	require.NoError(t, srv.RegisterSubscriber(context.Background(), "topic.live", "g1", false, func(context.Context, broker.Event) error { return nil }, nil))
	assert.Contains(t, b.snapshotSubscribed(), "topic.live", "Start 后登记应立即落订阅簿")

	require.NoError(t, srv.Stop(context.Background()))
	assert.Empty(t, srv.Endpoint(), "Stop 后端点必回空")

	require.NoError(t, srv.Stop(context.Background()), "重复 Stop 幂等")
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestServer_StopClearsDeferredQueue 锁定 Stop 清 deferred 的有意语义：
// Start 前登记的条目在 Start 时 flush；Stop 后重建不再 flush 旧条目
// （重建走 contract 新实例）。
func TestServer_StopClearsDeferredQueue(t *testing.T) {
	srv := NewServer()
	b := newStubBroker()
	srv.Broker = b

	require.NoError(t, srv.RegisterSubscriber(context.Background(), "topic.deferred", "g1", false, func(context.Context, broker.Event) error { return nil }, nil))
	require.NoError(t, srv.Start(context.Background()))
	assert.Contains(t, b.snapshotSubscribed(), "topic.deferred", "Start 时 deferred 队列应 flush")

	require.NoError(t, srv.Stop(context.Background()))
	require.NoError(t, srv.Start(context.Background()))
	assert.Contains(t, b.snapshotSubscribed(), "topic.deferred", "第二次 Start 只有显式登记的条目")

	deferredCount := 0
	for _, key := range b.snapshotSubscribed() {
		if key == "topic.deferred" {
			deferredCount++
		}
	}
	assert.Equal(t, 1, deferredCount, "Stop 清 deferred：重建不得重复 flush 旧条目")

	defer func() { _ = srv.Stop(context.Background()) }()
}

// ---- options 全表面：全部 ServerOption 执行且离线 Start 绿 ----

func TestServerOptions_AllFieldsApplied(t *testing.T) {
	srv := NewServer(
		WithAddress([]string{"127.0.0.1:9092"}),
		WithCodec("json"),
		WithTLSConfig(nil),
		WithPlainMechanism("user", "pass"),
		WithScramMechanism("SHA256", "user", "pass"),
		WithMetrics(&recordingMetrics{counters: map[string]float64{}}),
		WithBrokerOptions(broker.WithAddress("127.0.0.1:9092")),
	)
	assert.Equal(t, KindKafka, srv.Name(), "Name 来自家族参数化点")

	require.NoError(t, srv.Start(context.Background()), "全选项构造后离线 Start 应绿")
	defer func() { _ = srv.Stop(context.Background()) }()
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
// 选项 → kernel 计量面 → wrapHandler 三路计数（桩捕获 wrapped handler 直驱）。
func TestServer_MetricsOptionWired(t *testing.T) {
	m := &recordingMetrics{counters: map[string]float64{}}
	srv := NewServer(WithMetrics(m))
	b := newStubBroker()
	srv.Broker = b

	require.NoError(t, srv.RegisterSubscriber(context.Background(), "topic.m", "g1", false, func(context.Context, broker.Event) error { return nil }, nil))
	require.NoError(t, srv.Start(context.Background()))
	defer func() { _ = srv.Stop(context.Background()) }()

	wrapped, ok := b.captured["topic.m"]
	require.True(t, ok, "桩应捕获到包装后的 handler")
	_ = wrapped(context.Background(), stubEvent{msg: broker.NewMessage("x")})
	assert.Positive(t, m.counters["broker_messages_received_total"], "received 计数必须亮（WithMetrics 注入链生效）")
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

// ---- 泛型登记糖：deferred/live 两态 + disableAutoAck 分支（经桩 witness）----

func TestServer_RegisterSubscriberGeneric(t *testing.T) {
	srv := NewServer()
	b := newStubBroker()
	srv.Broker = b

	type payload struct {
		Value string `json:"value"`
	}
	handler := func(_ context.Context, _ string, _ broker.Headers, _ *payload) error { return nil }

	// Start 前（deferred）：disableAutoAck=true 走 WithSubscribeAutoAck(false) 分支。
	require.NoError(t, RegisterSubscriber(srv, context.Background(), "topic.generic", "g1", true, handler))
	require.NoError(t, srv.Start(context.Background()))
	defer func() { _ = srv.Stop(context.Background()) }()

	assert.Contains(t, b.snapshotSubscribed(), "topic.generic", "Start 时 deferred 队列应 flush 成订阅")
	var opts broker.SubscribeOptions
	opts.Apply(b.capturedOpt["topic.generic"]...)
	assert.Equal(t, "g1", opts.Queue, "queue 必须落进 Subscribe 选项")
	// 选项数见证 disableAutoAck 分支：ctx 头插 1 + queue 1 + AutoAck(false) 1 = 3。
	require.Len(t, b.capturedOpt["topic.generic"], 3, "disableAutoAck=true 必须追加 AutoAck(false)")

	// Start 后（live 路径）：disableAutoAck=false 分支（不追加 AutoAck(false)）。
	require.NoError(t, RegisterSubscriber(srv, context.Background(), "topic.generic.live", "g1", false, handler))
	var liveOpts broker.SubscribeOptions
	liveOpts.Apply(b.capturedOpt["topic.generic.live"]...)
	assert.Equal(t, "g1", liveOpts.Queue, "queue 仍必须落进 Subscribe 选项")
	require.Len(t, b.capturedOpt["topic.generic.live"], 2, "disableAutoAck=false 不得追加 AutoAck(false)（ctx+queue）")
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
	// kafka 的 NewServer 无默认选项（与 redis 不同）；非 nil TLS 追加
	// EnableSecure + TLSConfig 两条 → 2。
	require.Len(t, captured, 2)
}

// ---- 泛型 wrapper 两个分支：桩捕获 wrapped handler，合成事件直驱（离线无投递）----

func TestServer_GenericWrapperBranches(t *testing.T) {
	b := newStubBroker()
	srv := NewServer()
	srv.Broker = b

	type payload struct {
		Value string `json:"value"`
	}
	typedHits := 0
	require.NoError(t, RegisterSubscriber(srv, context.Background(), "topic.gen", "g1", false,
		func(_ context.Context, _ string, _ broker.Headers, p *payload) error {
			typedHits++
			return nil
		}))

	// Start flush deferred → stub 捕获 wrapped handler。
	require.NoError(t, srv.Start(context.Background()))
	wrapped, ok := b.captured["topic.gen"]
	require.True(t, ok, "stub 应捕获到包装后的 handler")

	// case *T 分支：Body 为 *payload → typed handler 驱动。
	require.NoError(t, wrapped(context.Background(), stubEvent{msg: broker.NewMessage(&payload{Value: "v"})}))
	assert.Equal(t, 1, typedHits, "typed handler 应被驱动一次")

	// default 分支：Body 非 *T → "unsupported type" 错误透传。
	err := wrapped(context.Background(), stubEvent{msg: broker.NewMessage("wrong-type")})
	require.ErrorContains(t, err, "unsupported type: string")
	assert.Equal(t, 1, typedHits, "default 分支不驱动 typed handler")
}
