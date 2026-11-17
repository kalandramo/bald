package rabbitmq

import (
	"context"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/kalandramo/bald/broker"
)

// refusedURL 给连接层用：init 只认带 amqp:// 前缀的地址，裸 host:port 会被
// 静默忽略、退回 DefaultRabbitURL，那样测试就可能在真 5672 端口上抖动。
func refusedURL(t *testing.T) string {
	t.Helper()
	return "amqp://" + refusedAddr(t)
}

// ---- init 的 exchange 分支 ----

func TestInitLegacyExchangeOverridesDefault(t *testing.T) {
	conn := newRabbitMQConnection(broker.NewOptionsAndApply(
		WithExchangeName("orders"),
		WithExchangeType("direct"),
		WithDurableExchange(),
	))
	if conn.defaultExchangeName != "orders" {
		t.Fatalf("defaultExchangeName = %q, want orders", conn.defaultExchangeName)
	}
	ex, err := conn.GetExchange("orders")
	if err != nil {
		t.Fatalf("GetExchange(orders): %v", err)
	}
	if ex.Type != "direct" || !ex.Durable {
		t.Fatalf("legacy exchange = %+v, want direct/durable", ex)
	}
	if _, err := conn.GetExchange(DefaultExchange.Name); err == nil {
		t.Fatal("legacy 改名后默认 exchange 应被摘除")
	}
}

func TestInitMultipleExchangesAndDefaultOverride(t *testing.T) {
	conn := newRabbitMQConnection(broker.NewOptionsAndApply(
		WithExchanges(
			Exchange{Name: "a", Type: "topic", Durable: true},
			Exchange{Name: "b", Type: "fanout"},
		),
	))
	if conn.defaultExchangeName != "a" {
		t.Fatalf("无 legacy 名时首个 exchange 应为默认，got %q", conn.defaultExchangeName)
	}
	if _, err := conn.GetExchange("b"); err != nil {
		t.Fatalf("GetExchange(b): %v", err)
	}

	conn2 := newRabbitMQConnection(broker.NewOptionsAndApply(
		WithExchanges(
			Exchange{Name: "a", Type: "topic"},
			Exchange{Name: "b", Type: "fanout"},
		),
		WithDefaultExchange("b"),
	))
	if conn2.defaultExchangeName != "b" {
		t.Fatalf("WithDefaultExchange 未生效，got %q", conn2.defaultExchangeName)
	}
	// 指向未注册 exchange 的 override 必须被忽略（不能留下悬空默认名）。
	conn3 := newRabbitMQConnection(broker.NewOptionsAndApply(
		WithExchanges(Exchange{Name: "a"}),
		WithDefaultExchange("ghost"),
	))
	if conn3.defaultExchangeName != "a" {
		t.Fatalf("悬空 default override 应忽略，got %q", conn3.defaultExchangeName)
	}
}

func TestGetExchangeEmptyFallsBack(t *testing.T) {
	conn := newRabbitMQConnection(broker.NewOptionsAndApply())
	if _, err := conn.GetExchange(""); err != nil {
		t.Fatalf("空名应回落默认 exchange: %v", err)
	}
	if _, err := conn.GetExchange("nope"); err == nil {
		t.Fatal("未注册 exchange 应报错")
	}
}

// ---- init 的 qos / confirm / callback 分支 ----

func TestInitQosConfirmAndCallbacks(t *testing.T) {
	onRet := func(amqp.Return) {}
	onConf := func(amqp.Confirmation) {}
	conn := newRabbitMQConnection(broker.NewOptionsAndApply(
		WithPrefetchCount(7),
		WithPrefetchSize(1024),
		WithPrefetchGlobal(),
		WithConfirmMode(),
		WithOnReturn(onRet),
		WithOnConfirm(onConf),
	))
	if conn.qos.PrefetchCount != 7 || conn.qos.PrefetchSize != 1024 || !conn.qos.PrefetchGlobal {
		t.Fatalf("qos = %+v, want 7/1024/global", conn.qos)
	}
	if !conn.confirmMode {
		t.Fatal("confirmMode 应为 true")
	}
	if conn.onReturn == nil || conn.onConfirm == nil {
		t.Fatal("onReturn/onConfirm 回调未注入")
	}
}

// ---- 构造期边界：nil ctx 经任一 WithXxx 后必须兜底为 Background ----

func TestNilContextFallsBackThroughOption(t *testing.T) {
	opts := broker.NewOptionsAndApply(
		broker.WithOptionContext(nil),
		WithExchangeName("x"), // OptionContextWithValue 内部对 nil Context 兜底
	)
	if opts.Context == nil {
		t.Fatal("WithXxx 应在 nil ctx 之后兜底 Background，避免 init 裸读 nil.Context")
	}
	if _, ok := opts.Context.Value(exchangeNameKey{}).(string); !ok {
		t.Fatal("exchangeName 未落到兜底后的 context")
	}
}

// ---- 每个 broker.Option 至少被调用一次（覆盖 options.go 全 0% 的函数体）----

func TestAllBrokerOptionsAreConstructible(t *testing.T) {
	broker.NewOptionsAndApply(
		WithDurableExchange(),
		WithExchangeName("ex"),
		WithExchangeType("topic"),
		WithPrefetchCount(1),
		WithPrefetchSize(2),
		WithPrefetchGlobal(),
		WithExternalAuth(),
		WithExchanges(Exchange{Name: "e1"}),
		WithDefaultExchange("e1"),
		WithDefaultQueue("q1"),
		WithConfirmMode(),
		WithOnReturn(func(amqp.Return) {}),
		WithOnConfirm(func(amqp.Confirmation) {}),
	)
}

func TestAllSubscribeOptionsAreConstructible(t *testing.T) {
	b := &rabbitBroker{}
	// 仅构造/应用，不触碰连接。
	opts := []broker.SubscribeOption{
		WithDurableQueue(),
		WithAutoDeleteQueue(),
		WithBindArguments(map[string]any{"x": "y"}),
		WithQueueArguments(map[string]any{"q": "v"}),
		WithRequeueOnError(),
		WithAckOnSuccess(),
		WithSubscribeExchange("ex"),
	}
	so := broker.SubscribeOptions{Context: context.Background()}
	for _, o := range opts {
		o(&so)
	}
	_ = b
}

func TestAllPublishOptionsAreConstructible(t *testing.T) {
	ts := time.Unix(1, 0)
	opts := []broker.PublishOption{
		WithDeliveryMode(2),
		WithPriority(1),
		WithContentType("text/plain"),
		WithContentEncoding("utf-8"),
		WithCorrelationID("cid"),
		WithReplyTo("rt"),
		WithExpiration("1000"),
		WithMessageId("mid"),
		WithTimestamp(ts),
		WithTypeMsg("tm"),
		WithUserID("uid"),
		WithAppID("aid"),
		WithPublishHeaders(map[string]any{"h": "v"}),
		WithPublishDeclareQueue("q", true, false, nil, nil),
		WithMandatory(),
		WithPublishExchange("ex"),
	}
	po := broker.PublishOptions{Context: context.Background()}
	for _, o := range opts {
		o(&po)
	}
}
