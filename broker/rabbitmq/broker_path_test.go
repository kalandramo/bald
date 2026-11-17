package rabbitmq

import (
	"context"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/kalandramo/bald/broker"
)

// offlineBroker 造一个注入了「未握手连接」的 broker：Subscribe/Disconnect/
// publish 都能走到，唯独 resubscribe 会阻塞在 select，靠 close(conn.close) 收尾。
func offlineBroker(t *testing.T, opts ...broker.Option) *rabbitBroker {
	t.Helper()
	b, ok := NewBroker(opts...).(*rabbitBroker)
	if !ok {
		t.Fatal("NewBroker 返回非 *rabbitBroker")
	}
	if err := b.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	b.conn = newRabbitMQConnection(b.options)
	t.Cleanup(func() { _ = b.conn.Close() }) // 唤醒阻塞中的 resubscribe
	return b
}

// ---- Subscribe 的路由键与默认队列守卫 ----

func TestSubscribeEmptyRoutingKeyWithoutDefault(t *testing.T) {
	b := offlineBroker(t)
	if _, err := b.Subscribe("", func(context.Context, broker.Event) error { return nil }, nil); err == nil {
		t.Fatal("空路由键且无 default queue 应报错")
	}
}

func TestSubscribeDefaultQueueFallback(t *testing.T) {
	b := offlineBroker(t, WithDefaultQueue("fallback.q"))
	sub, err := b.Subscribe("", func(context.Context, broker.Event) error { return nil }, nil)
	if err != nil {
		t.Fatalf("有 default queue 时 Subscribe 应成功: %v", err)
	}
	if got := sub.(*subscriber).topic; got != "fallback.q" {
		t.Fatalf("topic = %q, want fallback.q", got)
	}
}

// ---- SubscribeOption 落点 ----

func TestSubscribeOptionsApplied(t *testing.T) {
	b := offlineBroker(t)
	sub, err := b.Subscribe("rk.opts", func(context.Context, broker.Event) error { return nil }, nil,
		WithDurableQueue(),
		WithBindArguments(map[string]any{"bind": "v"}),
		WithQueueArguments(map[string]any{"queue": "v"}),
		WithSubscribeExchange("orders"),
		WithAutoDeleteQueue(), // 与 WithDurableQueue 互斥，应胜出
	)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	s := sub.(*subscriber)
	if s.autoDelete != true || s.durableQueue != false {
		t.Fatalf("WithAutoDeleteQueue 应胜出并清 durableQueue: auto=%v durable=%v", s.autoDelete, s.durableQueue)
	}
	if s.exchangeName != "orders" {
		t.Fatalf("exchangeName = %q, want orders", s.exchangeName)
	}
	if got := s.headers["bind"]; got != "v" {
		t.Fatalf("bind args = %v", got)
	}
	if got := s.queueArgs["queue"]; got != "v" {
		t.Fatalf("queue args = %v", got)
	}
	if s.Topic() != "rk.opts" || s.Options().AutoAck != true {
		t.Fatalf("默认 AutoAck 应为 true, topic=%q autoAck=%v", s.Topic(), s.Options().AutoAck)
	}
}

// ---- fn 消息处理链：binder / unmarshal 失败 / handler 返回值 / ack----

func TestSubscribeFnDeliveryBranches(t *testing.T) {
	cases := []struct {
		name       string
		binder     broker.Binder
		handlerErr error
		opts       []broker.SubscribeOption
	}{
		{"binder_nil", nil, nil, nil},
		{"binder_unmarshal_fail", func() any { return new(map[string]any) }, nil, nil},
		{"handler_error_noack", nil, errBoom, nil},
		{"ack_on_success", nil, nil, []broker.SubscribeOption{WithAckOnSuccess()}},
		{"nack_on_error", nil, errBoom, []broker.SubscribeOption{WithAckOnSuccess(), WithRequeueOnError()}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := offlineBroker(t)
			called := false
			sub, err := b.Subscribe("rk.fn", func(_ context.Context, evt broker.Event) error {
				called = true
				return c.handlerErr
			}, c.binder, c.opts...)
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			s := sub.(*subscriber)
			// Acknowledger 为 nil 时 Ack/Nack 返回 ErrClosed，不 panic。
			s.fn(amqp.Delivery{RoutingKey: "rk.fn", Body: []byte(`{"k":"v"}`)})
			if !called {
				t.Fatal("handler 未被驱动")
			}
			if s.IsClosed() {
				t.Fatal("未 Unsubscribe 时 IsClosed 应为 false")
			}
		})
	}
}

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom" }

// ---- Unsubscribe 幂等与字段收口 ----

func TestSubscriberUnsubscribe(t *testing.T) {
	b := offlineBroker(t)
	sub, err := b.Subscribe("rk.unsub", func(context.Context, broker.Event) error { return nil }, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	s := sub.(*subscriber)
	if err := s.Unsubscribe(false); err != nil {
		t.Fatalf("Unsubscribe（ch=nil）应幂等 nil, got %v", err)
	}
	if !s.IsClosed() {
		t.Fatal("Unsubscribe 后 IsClosed 应为 true")
	}
	// 二次退订仍幂等。
	if err := s.Unsubscribe(true); err != nil {
		t.Fatalf("二次 Unsubscribe: %v", err)
	}
}

// ---- SubscribeMiddleware 贯通 ----

func TestSubscribeMiddlewareWired(t *testing.T) {
	seen := 0
	b := offlineBroker(t, broker.WithSubscriberMiddlewares(
		func(next broker.Handler) broker.Handler {
			return func(ctx context.Context, evt broker.Event) error {
				seen++
				return next(ctx, evt)
			}
		},
	))
	sub, err := b.Subscribe("rk.mw", func(context.Context, broker.Event) error { return nil }, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	sub.(*subscriber).fn(amqp.Delivery{RoutingKey: "rk.mw", Body: []byte("x")})
	if seen != 1 {
		t.Fatalf("订阅中间件驱动次数 = %d, want 1", seen)
	}
}

// ---- Publish 层：body 透传经过 publish 各 option 分支，落到空壳 channel 守卫 ----

func TestPublishWithAllOptions(t *testing.T) {
	b := offlineBroker(t)
	b.conn.ExchangeChannel = &rabbitChannel{}
	err := b.Publish(context.Background(), "rk.pub", &broker.Message{Body: "payload"},
		WithDeliveryMode(2),
		WithPriority(3),
		WithContentType("text/plain"),
		WithContentEncoding("utf-8"),
		WithCorrelationID("cid"),
		WithReplyTo("rt"),
		WithExpiration("1000"),
		WithMessageId("mid"),
		WithTimestamp(time.Unix(1700000000, 0)),
		WithTypeMsg("tm"),
		WithUserID("uid"),
		WithAppID("aid"),
		WithPublishHeaders(map[string]any{"h": "v"}),
		WithMandatory(),
		WithPublishExchange("amq.topic"),
	)
	if err == nil || !strings.Contains(err.Error(), "channel is nil") {
		t.Fatalf("Publish with options = %v, want channel is nil", err)
	}
}

// ---- Publish 的 declare-queue 分支：ghost exchange 触发早退，不碰空壳 channel ----

func TestPublishDeclareQueueUnknownExchange(t *testing.T) {
	b := offlineBroker(t)
	b.conn.ExchangeChannel = &rabbitChannel{}
	err := b.Publish(context.Background(), "rk.dq", &broker.Message{Body: "x"},
		WithPublishExchange("ghost"),
		WithPublishDeclareQueue("q", true, false, nil, nil),
	)
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("declare queue 指向未注册 exchange: %v", err)
	}
}

// ---- Connect 带 EXTERNAL 认证：覆盖 externalAuth 注入分支 ----

func TestConnectWithExternalAuth(t *testing.T) {
	b, ok := NewBroker(broker.WithAddress(refusedAddr(t)), WithExternalAuth()).(*rabbitBroker)
	if !ok {
		t.Fatal("NewBroker 返回非 *rabbitBroker")
	}
	if err := b.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// 拒连地址保证拨号失败；这里只确认该分支不 panic、如实报错。
	if err := b.Connect(); err == nil {
		t.Fatal("带 EXTERNAL 认证指向拒连地址应失败")
	}
}

// ---- Disconnect 收尾：注入连接但无底层 amqp.Connection 时如实报错 ----

func TestDisconnectWithoutUnderlyingConnection(t *testing.T) {
	b := offlineBroker(t)
	if err := b.Disconnect(); err == nil {
		t.Fatal("无底层连接时 Disconnect 应报 connection is nil")
	}
}
