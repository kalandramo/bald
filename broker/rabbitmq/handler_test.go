package rabbitmq

import (
	"sync"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/kalandramo/bald/broker"
)

// ---- handleConfirms：自定义 onConfirm 与默认 log 两分支 ----

func TestHandleConfirmsCustomHandler(t *testing.T) {
	r := newRabbitMQConnection(broker.NewOptionsAndApply())
	ch := make(chan amqp.Confirmation, 4)

	var mu sync.Mutex
	var tags []uint64
	r.onConfirm = func(c amqp.Confirmation) {
		mu.Lock()
		tags = append(tags, c.DeliveryTag)
		mu.Unlock()
	}

	done := make(chan struct{})
	go func() { r.handleConfirms(ch); close(done) }()

	ch <- amqp.Confirmation{DeliveryTag: 1, Ack: true}
	ch <- amqp.Confirmation{DeliveryTag: 2, Ack: false}
	close(ch)
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(tags) != 2 || tags[0] != 1 || tags[1] != 2 {
		t.Fatalf("onConfirm 收到 %v, want [1 2]", tags)
	}
}

func TestHandleConfirmsDefaultLogBranch(t *testing.T) {
	// onConfirm=nil 走默认 logAck/logNack；log 默认 nop，不得 panic。
	r := newRabbitMQConnection(broker.NewOptionsAndApply())
	ch := make(chan amqp.Confirmation, 2)
	ch <- amqp.Confirmation{DeliveryTag: 1, Ack: true}
	ch <- amqp.Confirmation{DeliveryTag: 2, Ack: false}
	close(ch)
	done := make(chan struct{})
	go func() { r.handleConfirms(ch); close(done) }()
	<-done
}

// ---- handleReturns：自定义 onReturn 与默认 log 两分支 ----

func TestHandleReturnsCustomHandler(t *testing.T) {
	r := newRabbitMQConnection(broker.NewOptionsAndApply())
	ch := make(chan amqp.Return, 2)

	var mu sync.Mutex
	var reasons []string
	r.onReturn = func(ret amqp.Return) {
		mu.Lock()
		reasons = append(reasons, ret.ReplyText)
		mu.Unlock()
	}

	done := make(chan struct{})
	go func() { r.handleReturns(ch); close(done) }()

	ch <- amqp.Return{ReplyText: "NO_ROUTE", Exchange: "ex", RoutingKey: "rk"}
	close(ch)
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(reasons) != 1 || reasons[0] != "NO_ROUTE" {
		t.Fatalf("onReturn 收到 %v, want [NO_ROUTE]", reasons)
	}
}

func TestHandleReturnsDefaultLogBranch(t *testing.T) {
	r := newRabbitMQConnection(broker.NewOptionsAndApply())
	ch := make(chan amqp.Return, 1)
	ch <- amqp.Return{ReplyText: "NO_ROUTE"}
	close(ch)
	done := make(chan struct{})
	go func() { r.handleReturns(ch); close(done) }()
	<-done
}

// ---- 确认/返回回调经 Option 注入后确实贯通到连接字段 ----

func TestConfirmAndReturnOptionsWireThrough(t *testing.T) {
	confirmHit, returnHit := false, false
	conn := newRabbitMQConnection(broker.NewOptionsAndApply(
		WithConfirmMode(),
		WithOnConfirm(func(amqp.Confirmation) { confirmHit = true }),
		WithOnReturn(func(amqp.Return) { returnHit = true }),
	))
	cc := make(chan amqp.Confirmation, 1)
	rc := make(chan amqp.Return, 1)
	cc <- amqp.Confirmation{Ack: true}
	rc <- amqp.Return{}
	close(cc)
	close(rc)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); conn.handleConfirms(cc) }()
	go func() { defer wg.Done(); conn.handleReturns(rc) }()
	wg.Wait()

	if !confirmHit || !returnHit {
		t.Fatalf("Option 注入的回调未贯通: confirm=%v return=%v", confirmHit, returnHit)
	}
}

// ---- auth.go：EXTERNAL SASL 机制的两个访问器 ----

func TestExternalAuthentication(t *testing.T) {
	auth := &ExternalAuthentication{}
	if got := auth.Mechanism(); got != "EXTERNAL" {
		t.Fatalf("Mechanism() = %q, want EXTERNAL", got)
	}
	if got := auth.Response(); got != "" {
		t.Fatalf("Response() = %q, want empty", got)
	}
}

// ---- generateUUID：uuid.NewRandom 纯离线 ----

func TestGenerateUUID(t *testing.T) {
	a, b := generateUUID(), generateUUID()
	if a == "" || b == "" {
		t.Fatal("generateUUID 不应返回空串")
	}
	if a == b {
		t.Fatalf("两次生成不应相同: %q", a)
	}
}

// ---- logger.go：四个日志入口在默认 nop 后端下均不得 panic ----

func TestLoggerHelpersDoNotPanic(t *testing.T) {
	logError("boom")
	logDebugf("debug %d", 1)
	logWarnf("warn %s", "x")
	logErrorf("error %s", "y")
}

// ---- publication 访问器：无集群也能全绿 ----

func TestPublicationAccessors(t *testing.T) {
	msg := &broker.Message{Body: "payload"}
	wantErr := amqp.ErrClosed
	p := &publication{
		d:       amqp.Delivery{RoutingKey: "rk"},
		message: msg,
		topic:   "topic.a",
		err:     wantErr,
	}
	if got := p.Topic(); got != "topic.a" {
		t.Fatalf("Topic() = %q", got)
	}
	if p.Message() != msg {
		t.Fatal("Message() 应返回同一指针")
	}
	if got := p.RawMessage().(amqp.Delivery).RoutingKey; got != "rk" {
		t.Fatalf("RawMessage() routing key = %q", got)
	}
	if p.Error() != wantErr {
		t.Fatalf("Error() = %v, want %v", p.Error(), wantErr)
	}
	// d.Acknowledger 为 nil 时 Ack 返回 ErrDeliveryNotInitialized，不 panic。
	if err := p.Ack(); err != amqp.ErrDeliveryNotInitialized {
		t.Fatalf("无 Acknowledger 的 Ack = %v, want ErrDeliveryNotInitialized", err)
	}
}
