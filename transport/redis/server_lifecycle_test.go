package redis

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/encoding"
	jsoncodec "github.com/kalandramo/bald/encoding/json"
	"github.com/kalandramo/bald/transport/subscribe"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 编码器显式注册（默认 codec 惰性查表——测试二进制里没人注册就查不到）。
func init() {
	encoding.MustRegister(jsoncodec.New())
}

func (b *stubRedisBroker) snapshotSubscribed() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.subscribed...)
}

// ---- 桩：offline 验证 doRegisterSubscriberMap 的收敛语义 ----

type stubRedisBroker struct {
	broker.Broker // 只被 Subscribe 走到，其余方法不触碰
	failTopics    map[string]bool
	mu            sync.Mutex
	subscribed    []string
}

func (b *stubRedisBroker) Subscribe(topic string, _ broker.Handler, _ broker.Binder, _ ...broker.SubscribeOption) (broker.Subscriber, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failTopics[topic] {
		return nil, fmt.Errorf("stub: subscribe %s forced failure", topic)
	}
	b.subscribed = append(b.subscribed, topic)
	return &stubRedisSubscriber{}, nil
}

type stubRedisSubscriber struct{}

func (s *stubRedisSubscriber) Options() broker.SubscribeOptions { return broker.SubscribeOptions{} }
func (s *stubRedisSubscriber) Topic() string                    { return "" }
func (s *stubRedisSubscriber) Unsubscribe(bool) error           { return nil }

// TestServer_PartialRegistrationFailureKeepsDeferred 锁定收敛语义（段2 假设1）：
// doRegisterSubscriberMap 部分失败时，失败条目必须留在 subscriberOpts 里供下次
// Start 重试，成功条目才摘除——旧实现无条件清空整批 deferred 队列，Start 失败后
// 重试再也找不到待注册项，订阅静默丢失（传输层设计 §已知缺口 #2）。
func TestServer_PartialRegistrationFailureKeepsDeferred(t *testing.T) {
	b := &stubRedisBroker{failTopics: map[string]bool{"bad": true}}
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

	// 成功条目已摘除并落订阅簿；失败条目留在 deferred 队列供重试。
	if _, ok := s.subscriberOpts["good"]; ok {
		t.Fatal("成功条目应从 deferred 队列摘除")
	}
	require.Contains(t, s.subscriberOpts, "bad", "失败条目必须保留待重试")
	assert.Contains(t, b.snapshotSubscribed(), "good")
	assert.NotContains(t, b.snapshotSubscribed(), "bad")

	// 修复缺陷后重试：换不失败的 broker，bad 收敛补齐。
	b2 := &stubRedisBroker{}
	s.Broker = b2
	require.NoError(t, s.doRegisterSubscriberMap())
	assert.Contains(t, b2.snapshotSubscribed(), "bad")
	if _, ok := s.subscriberOpts["bad"]; ok {
		t.Fatal("重试成功后 deferred 队列应清空")
	}
}

// ---- miniredis 生命周期族 ----

func newLiveServer(t *testing.T, mr *miniredis.Miniredis, opts ...ServerOption) *Server {
	t.Helper()
	srv := NewServer(append([]ServerOption{WithAddress(mr.Addr())}, opts...)...)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	return srv
}

func TestServer_StartStopEndpointLifecycle(t *testing.T) {
	mr := miniredis.RunT(t)
	srv := newLiveServer(t, mr)

	require.NoError(t, srv.Start(context.Background()))

	ep := srv.Endpoint()
	require.NotEmpty(t, ep, "Start 后端点应为描述性值")
	require.True(t, strings.HasPrefix(ep, "redis://"), "端点应带 scheme: %q", ep)

	require.NoError(t, srv.Stop(context.Background()))
	assert.Empty(t, srv.Endpoint(), "Stop 后端点必回空")
}

func TestServer_DeferredRegistrationRoundtrip(t *testing.T) {
	mr := miniredis.RunT(t)
	srv := newLiveServer(t, mr)

	type payload struct {
		Value string `json:"value"`
	}
	got := make(chan string, 1)
	// Start 前登记（deferred 队列路径）。
	require.NoError(t, RegisterSubscriber(srv, "topic.deferred",
		func(_ context.Context, _ string, _ broker.Headers, p *payload) error {
			got <- p.Value
			return nil
		}))

	require.NoError(t, srv.Start(context.Background()))

	// 等订阅生效后发布。
	time.Sleep(200 * time.Millisecond)
	require.NoError(t, srv.Publish(context.Background(), "topic.deferred",
		&broker.Message{Body: payload{Value: "hello"}}))

	select {
	case v := <-got:
		assert.Equal(t, "hello", v)
	case <-time.After(3 * time.Second):
		t.Fatal("deferred 订阅未收到消息——Start 后订阅没有生效")
	}
}

func TestServer_RegisterSubscriberAfterStart(t *testing.T) {
	mr := miniredis.RunT(t)
	srv := newLiveServer(t, mr)
	require.NoError(t, srv.Start(context.Background()))

	type payload struct {
		Value string `json:"value"`
	}
	got := make(chan string, 1)
	require.NoError(t, RegisterSubscriber(srv, "topic.live",
		func(_ context.Context, _ string, _ broker.Headers, p *payload) error {
			got <- p.Value
			return nil
		}))

	time.Sleep(200 * time.Millisecond)
	require.NoError(t, srv.Publish(context.Background(), "topic.live",
		&broker.Message{Body: payload{Value: "live"}}))

	select {
	case v := <-got:
		assert.Equal(t, "live", v)
	case <-time.After(3 * time.Second):
		t.Fatal("Start 后直接登记的订阅未收到消息")
	}
}

func TestServer_StartFailureSticky(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:1")) // 拒连端口

	err := srv.Start(context.Background())
	require.Error(t, err, "拒连地址 Start 必失败")

	// s.err 粘滞：第二次 Start 直接回同一错误，不再重试建连。
	err2 := srv.Start(context.Background())
	require.Error(t, err2)
	require.Equal(t, err.Error(), err2.Error(), "粘滞错误应一致")
}
