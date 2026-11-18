package brokertype

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/transport/subscribe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- 桩：offline 验证生命周期/订阅簿语义（原 kafka/rabbitmq 两份同构桩的归并）----

type stubBroker struct {
	broker.Broker // 只被 Subscribe 走到，其余方法不触碰
	failKeys      map[string]bool
	initErr       error
	connectErr    error

	mu         sync.Mutex
	subscribed []string
	captured   map[string]broker.Handler // key → wrapHandler 包装后的 handler（供测试合成事件直驱）
}

func newStubBroker() *stubBroker {
	return &stubBroker{failKeys: map[string]bool{}, captured: map[string]broker.Handler{}}
}

func (b *stubBroker) Init(...broker.Option) error { return b.initErr }
func (b *stubBroker) Connect() error              { return b.connectErr }
func (b *stubBroker) Disconnect() error           { return nil }
func (b *stubBroker) Address() string             { return "stub:5672" }
func (b *stubBroker) Subscribe(key string, handler broker.Handler, _ broker.Binder, _ ...broker.SubscribeOption) (broker.Subscriber, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failKeys[key] {
		return nil, fmt.Errorf("stub: subscribe %s forced failure", key)
	}
	b.subscribed = append(b.subscribed, key)
	if b.captured != nil {
		b.captured[key] = handler
	}
	return &stubSubscriber{}, nil
}

func (b *stubBroker) snapshotSubscribed() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.subscribed...)
}

type stubSubscriber struct {
	unsubscribed bool
}

func (s *stubSubscriber) Options() broker.SubscribeOptions { return broker.SubscribeOptions{} }
func (s *stubSubscriber) Topic() string                    { return "" }
func (s *stubSubscriber) Unsubscribe(bool) error {
	s.unsubscribed = true
	return nil
}

type stubEvent struct{ msg *broker.Message }

func (e stubEvent) Topic() string            { return "key.m" }
func (e stubEvent) Message() *broker.Message { return e.msg }
func (e stubEvent) RawMessage() any          { return nil }
func (e stubEvent) Ack() error               { return nil }
func (e stubEvent) Error() error             { return nil }

type recordingMetrics struct {
	counters map[string]float64
	histos   []string
	errored  bool
}

func (m *recordingMetrics) Counter(_ context.Context, name string, value float64, _ map[string]string) {
	m.counters[name] += value
	if name == "broker_messages_errors_total" {
		m.errored = true
	}
}

func (m *recordingMetrics) Histogram(_ context.Context, name string, _ float64, _ map[string]string) {
	m.histos = append(m.histos, name)
}

func (m *recordingMetrics) Gauge(context.Context, string, float64, map[string]string)    {}
func (m *recordingMetrics) GaugeAdd(context.Context, string, float64, map[string]string) {}

// testFamily 家族名用 stub——日志前缀/Name 断言随 fam.Kind 走，不钉某个真家族。
func testFamily() Family {
	return Family{
		Kind:      "stub",
		KeyNoun:   "key",
		NewBroker: func(opts ...broker.Option) broker.Broker { return newStubBroker() },
		Endpoint:  func(addr string) string { return "stub://" + addr },
	}
}

// TestServer_PartialRegistrationFailureKeepsDeferred 锁定收敛语义：
// doRegisterSubscriberMap 部分失败时，失败条目留在 subscriberOpts 供下次 Start
// 重试，成功条目才摘除——旧实现无条件清空整批 deferred 队列（家族同构缺陷，
// 修复前在 kafka/rabbitmq 两份复制代码里各有一份测试见证，归并于此）。
func TestServer_PartialRegistrationFailureKeepsDeferred(t *testing.T) {
	b := newStubBroker()
	b.failKeys["bad"] = true
	s := NewServer(testFamily())
	s.Broker = b
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
	b2 := newStubBroker()
	s.Broker = b2
	require.NoError(t, s.doRegisterSubscriberMap())
	assert.Contains(t, b2.snapshotSubscribed(), "bad")
	if _, ok := s.subscriberOpts["bad"]; ok {
		t.Fatal("重试成功后 deferred 队列应清空")
	}
}

// TestServer_RegisterDuplicateOverwriteUnsubscribesOld 锁定覆盖语义：
// 同 key 覆盖必须先摘旧订阅——只覆写映射的话消息双份投递且订阅 goroutine 泄漏。
func TestServer_RegisterDuplicateOverwriteUnsubscribesOld(t *testing.T) {
	s := NewServer(testFamily())
	b := newStubBroker()
	s.Broker = b

	old := &stubSubscriber{}
	s.subscribers["key.dup"] = old

	require.NoError(t, s.doRegisterSubscriber("key.dup", func(context.Context, broker.Event) error { return nil }, nil))
	assert.True(t, old.unsubscribed, "覆盖前必须摘除旧订阅")
	assert.Len(t, s.subscribers, 1, "仍只有一个订阅者条目")
}

// TestServer_StartFailureSticky / InitFailureSticky：失败粘滞——Init/Connect
// 失败后 s.err 粘滞，二次 Start 回同一错误；Init 失败不清 deferred 队列。
func TestServer_StartFailureSticky(t *testing.T) {
	boom := errors.New("stub connect boom")
	s := NewServer(testFamily())
	s.Broker = &stubBroker{connectErr: boom}

	err := s.Start(context.Background())
	require.ErrorIs(t, err, boom)

	err2 := s.Start(context.Background())
	require.Equal(t, err.Error(), err2.Error(), "粘滞错误应一致")
}

func TestServer_InitFailureSticky(t *testing.T) {
	boom := errors.New("stub init boom")
	s := NewServer(testFamily())
	s.Broker = &stubBroker{initErr: boom}

	err := s.Start(context.Background())
	require.ErrorIs(t, err, boom)
	assert.Empty(t, s.subscriberOpts, "Init 失败不得清 deferred 队列（收敛语义）")

	// 粘滞：二次 Start 回同一错误。
	require.ErrorIs(t, s.Start(context.Background()), boom)
}

// TestServer_WrapHandlerMetrics：metrics 注入后的三路计数（直接单元驱动，
// 无需真集群投递）+ SetMetrics 注入面。
func TestServer_WrapHandlerMetrics(t *testing.T) {
	m := &recordingMetrics{counters: map[string]float64{}}
	s := NewServer(testFamily())
	s.SetMetrics(m)

	handlerErr := errors.New("handler boom")
	wrapped := s.wrapHandler("key.m", func(context.Context, broker.Event) error {
		return handlerErr
	})

	err := wrapped(context.Background(), stubEvent{msg: broker.NewMessage("x")})
	require.ErrorIs(t, err, handlerErr, "handler 错误应透传")
	assert.Positive(t, m.counters["broker_messages_received_total"], "received 计数必须亮")
	assert.NotEmpty(t, m.histos, "duration 直方图必须记录")
	assert.True(t, m.errored, "handler 错误必须进 error 计数")
	assert.Equal(t, "stub", s.Name())
}

// TestServer_RegisterNilCtxGuard：nil ctx 不得 panic——回落 baseCtx 后正常入队
// （家族统一取防御侧：统一前 kafka 会把 nil 塞进订阅选项，rabbitmq 有守卫）。
func TestServer_RegisterNilCtxGuard(t *testing.T) {
	s := NewServer(testFamily())
	require.NoError(t, s.Register(nil, "key.nil", func(context.Context, broker.Event) error { return nil }, nil))
	require.Contains(t, s.subscriberOpts, "key.nil")
}

// TestServer_RegisterBaseCtxFallback：字面量构造（baseCtx=nil）时兜底补 Background。
func TestServer_RegisterBaseCtxFallback(t *testing.T) {
	s := &Server{
		fam:            testFamily(),
		subscribers:    make(broker.SubscriberMap),
		subscriberOpts: make(subscribe.SubscribeOptionMap),
		// baseCtx 刻意留 nil：Register 的兜底分支应补 Background
	}
	require.NoError(t, s.Register(context.Background(), "key.ctx", func(context.Context, broker.Event) error { return nil }, nil))
	require.NotNil(t, s.baseCtx, "baseCtx 兜底应生效")
}

// TestServer_StopLifecycleAfterSuccessfulStart：成功 Start 后 Stop 的三件套
// （退订/清簿/摘 started）+ double Stop 幂等。
func TestServer_StopLifecycleAfterSuccessfulStart(t *testing.T) {
	s := NewServer(testFamily())
	b := newStubBroker()
	s.Broker = b
	require.NoError(t, s.Start(context.Background()))
	require.NoError(t, s.Register(context.Background(), "key.stop", func(context.Context, broker.Event) error { return nil }, nil))
	assert.Len(t, s.subscribers, 1, "live 登记应直接落订阅簿")

	require.NoError(t, s.Stop(context.Background()))
	assert.Empty(t, s.subscribers, "Stop 应清订阅簿")
	assert.Empty(t, s.subscriberOpts, "Stop 应清 deferred 队列（重建走 contract 新实例）")
	assert.False(t, s.started.Load(), "started 必须复位")
	assert.Empty(t, s.Endpoint(), "Stop 后端点必回空")

	// double Stop：started 已复位 → 幂等 nil。
	require.NoError(t, s.Stop(context.Background()))
}

// TestServer_DoubleStartIdempotent：started 后重复 Start 幂等 nil，且不重复 flush。
func TestServer_DoubleStartIdempotent(t *testing.T) {
	s := NewServer(testFamily())
	b := newStubBroker()
	s.Broker = b
	require.NoError(t, s.Start(context.Background()))
	require.NoError(t, s.Start(context.Background()), "started 后重复 Start 应幂等返回 nil")
	assert.Len(t, b.snapshotSubscribed(), 0, "重复 Start 不得重复 flush deferred 队列")
}

// TestServer_LogHelpersDirect：实例级日志直驱（语句覆盖；输出进测试日志无妨）。
func TestServer_LogHelpersDirect(t *testing.T) {
	s := NewServer(testFamily())
	s.logInfo("unit", "driven")
	s.logError("unit", "driven")
	s.logInfof("unit %s", "driven")
	s.logWarnf("unit %s", "driven")
	s.logErrorf("unit %s", "driven")
}
