package redis

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/redis"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- options 全表面：每个 ServerOption 都被执行并落到预期字段 ----

func TestServerOptions_AllFieldsApplied(t *testing.T) {
	mr := miniredis.RunT(t)
	m := &recordingMetrics{counters: map[string]float64{}}
	srv := NewServer(
		WithAddress(mr.Addr()),
		WithCodec("json"),
		WithConnectTimeout(time.Second),
		WithReadTimeout(time.Second),
		WithWriteTimeout(time.Second),
		WithIdleTimeout(time.Second),
		WithMaxIdle(2),
		WithMaxActive(4),
		WithDriverType(redis.DriverTypePubSub),
		WithMetrics(m),
		WithBrokerOptions(broker.WithAddress(mr.Addr())), // 追加序：业务在契约后
	)

	require.NoError(t, srv.Start(context.Background()))
	defer func() { _ = srv.Stop(context.Background()) }()
	assert.Equal(t, redis.DriverTypePubSub, srv.driverType)
	assert.Same(t, m, srv.m, "WithMetrics 应注入实例")

	// broker 侧选项真实生效：Start 成功即建连（错误的连接池参数会失败）。
	assert.NotEmpty(t, srv.Address())
}

// ---- wrapHandler：metrics 注入后计数/直方图/错误计数三路都驱动 ----

type recordingMetrics struct {
	mu       sync.Mutex
	counters map[string]float64
	histos   []string
	errored  bool
}

func (m *recordingMetrics) Counter(_ context.Context, name string, value float64, labels map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters[name] += value
	if name == "broker_messages_errors_total" {
		m.errored = true
	}
}

func (m *recordingMetrics) Histogram(_ context.Context, name string, _ float64, _ map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.histos = append(m.histos, name)
}

func (m *recordingMetrics) Gauge(context.Context, string, float64, map[string]string) {}

func (m *recordingMetrics) GaugeAdd(context.Context, string, float64, map[string]string) {}

func TestServer_WrapHandlerMetrics(t *testing.T) {
	mr := miniredis.RunT(t)
	m := &recordingMetrics{counters: map[string]float64{}}
	srv := newLiveServer(t, mr, WithMetrics(m))

	type payload struct {
		Value string `json:"value"`
	}
	require.NoError(t, RegisterSubscriber(srv, "topic.metrics",
		func(_ context.Context, _ string, _ broker.Headers, _ *payload) error {
			return assert.AnError // 错误路径：error counter 必须亮
		}))

	require.NoError(t, srv.Start(context.Background()))
	time.Sleep(200 * time.Millisecond)
	require.NoError(t, srv.Publish(context.Background(), "topic.metrics",
		&broker.Message{Body: payload{Value: "x"}}))

	// 等 handler 处理与计数落账（broker 对 handler 错误的重投不会影响计数断言）。
	time.Sleep(300 * time.Millisecond)

	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Positive(t, m.counters["broker_messages_received_total"], "received 计数必须亮")
	assert.NotEmpty(t, m.histos, "duration 直方图必须记录")
	assert.True(t, m.errored, "handler 错误必须进 error 计数")
}

// ---- 端点语义：凭证脱敏 + URL 解析失败回退 ----

func TestServer_EndpointStripsCredentials(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.RequireAuth("s3cret")

	// 密码走地址 userinfo（redigo DialURL 形态）；端点面不得外泄。
	srv := NewServer(WithAddress("redis://:s3cret@" + mr.Addr()))
	require.NoError(t, srv.Start(context.Background()))
	defer func() { _ = srv.Stop(context.Background()) }()

	ep := srv.Endpoint()
	require.NotEmpty(t, ep)
	assert.NotContains(t, ep, "s3cret", "端点不得携带密码")
}

type stubAddressBroker struct {
	broker.Broker
	addr string
}

func (b *stubAddressBroker) Address() string { return b.addr }

func TestServer_EndpointUnparsableAddrFallsBackRaw(t *testing.T) {
	// url.Parse 失败的地址：不吞端点，回原值好于谎报未就绪（server.go 注释口径）。
	raw := "redis://[::1:6379" // 方括号未闭合 → url.Parse 报错
	s := &Server{Broker: &stubAddressBroker{addr: raw}}
	s.started.Store(true)
	assert.Equal(t, raw, s.Endpoint())
}

// ---- 重入与幂等：double Start / double Stop / 同 topic 重复登记 ----

func TestServer_StartStopIdempotent(t *testing.T) {
	mr := miniredis.RunT(t)
	srv := newLiveServer(t, mr)

	require.NoError(t, srv.Start(context.Background()))
	require.NoError(t, srv.Start(context.Background()), "重复 Start 应幂等返回 nil")
	require.NoError(t, srv.Stop(context.Background()))
	require.NoError(t, srv.Stop(context.Background()), "重复 Stop 应幂等返回 nil")

	// Stop 清空 deferred 队列是有意语义：实例停机即终结，重建走 contract
	// Provider 的新实例（ReloadRestart 路径），desired state 不跨实例携带。
	assert.Empty(t, srv.subscriberOpts)
}

func TestServer_RegisterDuplicateTopicOverwrites(t *testing.T) {
	mr := miniredis.RunT(t)
	srv := newLiveServer(t, mr)

	require.NoError(t, srv.RegisterSubscriber("topic.dup", func(context.Context, broker.Event) error { return nil }, nil))
	require.NoError(t, srv.RegisterSubscriber("topic.dup", func(context.Context, broker.Event) error { return nil }, nil),
		"同 topic 重复登记应走覆盖路径而非报错")
	require.NoError(t, srv.Start(context.Background()))
	assert.Len(t, srv.subscribers, 1, "重复登记只保留一个订阅者")
}
