package rabbitmq

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/kalandramo/bald/broker"
)

// shellConn 造一个带空壳 publish channel 的连接：ExchangeChannel 非 nil，
// lazyInitPublishChannel 会跳过建通道；pubCh.Publish 命中 channel==nil 守卫，
// 于是 Publish 整条错误链可离线走通，不需要真 AMQP 握手。
func shellConn() *rabbitConnection {
	r := newRabbitMQConnection(broker.NewOptionsAndApply())
	r.ExchangeChannel = &rabbitChannel{}
	return r
}

// ---- Publish 错误链：持锁快照 pubCh + channel nil 守卫 ----

func TestPublishReturnsChannelNilError(t *testing.T) {
	r := shellConn()
	err := r.Publish(context.Background(), "amq.topic", "rk", false, amqp.Publishing{Body: []byte("x")})
	if err == nil || !strings.Contains(err.Error(), "channel is nil") {
		t.Fatalf("空壳 publish channel 的 Publish = %v, want channel is nil", err)
	}
}

// TestPublishConcurrentWithReconnectWrite 钉住 C-5：reconnect/tryConnect 会
// 持锁置换 ExchangeChannel，Publish 必须持锁取快照后再用。写侧只替换为新的
// 非空壳指针（绝不置 nil，避免测试自身解引用 nil），读侧并发 Publish。
// 若读侧退回裸字段读，-race 必报数据竞争；持锁快照则全绿且错误语义不坍塌。
func TestPublishConcurrentWithReconnectWrite(t *testing.T) {
	r := shellConn()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const goroutines = 8
	const perG = 50
	errs := make([]error, goroutines*perG)

	// 写侧：模拟重连风暴窗口内反复置换 ExchangeChannel（始终非空壳）。
	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			r.Lock()
			r.ExchangeChannel = &rabbitChannel{}
			r.Unlock()
		}
	}()

	var readers sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		readers.Add(1)
		go func(g int) {
			defer readers.Done()
			for i := 0; i < perG; i++ {
				errs[g*perG+i] = r.Publish(ctx, "amq.topic", "rk", false, amqp.Publishing{})
			}
		}(g)
	}
	readers.Wait()
	close(stop)
	writer.Wait()

	for i, err := range errs {
		if err == nil || !strings.Contains(err.Error(), "channel is nil") {
			t.Fatalf("errs[%d] = %v, want channel is nil", i, err)
		}
	}
}

// TestPublishMiddlewareChainAndMessageHeaders 覆盖 Publish 的两条真实功能路径：
// broker 级 PublishMiddlewares 链，以及 broker.Message.Headers 向 amqp.Publishing
// 的透传。使用 offlineBroker（无底层连接），发布最终在源头 nil 守卫处以
// ErrNotConnected 收尾；中间件仍应被调用一次，证明它被接入了发布链。
func TestPublishMiddlewareChainAndMessageHeaders(t *testing.T) {
	called := 0
	b := offlineBroker(t, broker.WithPublishMiddlewares(
		func(next broker.PublishHandler) broker.PublishHandler {
			return func(ctx context.Context, topic string, msg *broker.Message, opts ...broker.PublishOption) error {
				called++
				return next(ctx, topic, msg, opts...)
			}
		},
	))

	err := b.Publish(context.Background(), "rk.mw", &broker.Message{
		Body:    "payload",
		Headers: broker.Headers{"h1": "v1", "h2": "v2"},
	})
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Publish = %v, want errors.Is(_, ErrNotConnected)", err)
	}
	if called != 1 {
		t.Fatalf("Publish 中间件调用次数 = %d, want 1", called)
	}
}

// ---- DeclarePublishQueue：GetExchange 错误分支（空壳下安全）----

func TestDeclarePublishQueueUnknownExchange(t *testing.T) {
	r := shellConn()
	err := r.DeclarePublishQueue("ghost", "q", "rk", nil, nil, false, false)
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("未注册 exchange 应报错, got %v", err)
	}
}

// ---- Consume：GetExchange 错误分支（不触碰 Connection）----

func TestConsumeUnknownExchange(t *testing.T) {
	r := shellConn()
	_, _, err := r.Consume("ghost", "q", "rk", nil, nil, true, true, false)
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("未注册 exchange 应报错, got %v", err)
	}
}

// ---- channel.go 的 nil 守卫方法：空壳即可覆盖 ----

func TestRabbitChannelNilGuards(t *testing.T) {
	ch := &rabbitChannel{}
	if err := ch.Close(); err == nil || !strings.Contains(err.Error(), "channel is nil") {
		t.Fatalf("空的 rabbitChannel.Close = %v", err)
	}
	if err := ch.Publish(context.Background(), "ex", "rk", false, amqp.Publishing{}); err == nil {
		t.Fatal("空的 rabbitChannel.Publish 应报错")
	}
	if err := ch.Confirm(); err == nil {
		t.Fatal("空的 rabbitChannel.Confirm 应报错")
	}
	if got := ch.NotifyPublish(make(chan amqp.Confirmation)); got != nil {
		t.Fatalf("空的 rabbitChannel.NotifyPublish = %v, want nil", got)
	}
	if got := ch.NotifyReturn(make(chan amqp.Return)); got != nil {
		t.Fatalf("空的 rabbitChannel.NotifyReturn = %v, want nil", got)
	}
}

// TestUnconnectedBrokerPublishReturnsNotConnected 是原 KnownDefect 假门禁的正向
// 回归：同一复现步骤（拒连地址 → Init → Connect 失败 → Publish）现在必须得到
// 明确的 ErrNotConnected，而不是把 panic 钉成期望值。
func TestUnconnectedBrokerPublishReturnsNotConnected(t *testing.T) {
	b, ok := NewBroker(broker.WithAddress(refusedAddr(t))).(*rabbitBroker)
	if !ok {
		t.Fatal("NewBroker 返回非 *rabbitBroker")
	}
	if err := b.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := b.Connect(); err == nil {
		t.Fatal("拒连地址 Connect 应失败（本测试依赖连接失败后的半连接态）")
	}
	// 前置条件钉住半连接态：wrapper 存在、底层连接未建立、connected 未置真。
	if b.conn == nil {
		t.Fatal("Connect 失败后 conn wrapper 应已创建")
	}
	if b.conn.isConnected() {
		t.Fatal("Connect 失败后不应处于已连接态")
	}

	err := b.Publish(context.Background(), "rk.defect", &broker.Message{Body: "x"})
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("半连接态 Publish = %v, want errors.Is(_, ErrNotConnected)", err)
	}
}

// TestUnconnectedBrokerConsumeReturnsNotConnected 是消费侧的对偶回归：同一
// 半连接态下 Consume 不得在 newRabbitChannel(nil) 处 panic，须如实返回
// ErrNotConnected。
func TestUnconnectedBrokerConsumeReturnsNotConnected(t *testing.T) {
	b, ok := NewBroker(broker.WithAddress(refusedAddr(t))).(*rabbitBroker)
	if !ok {
		t.Fatal("NewBroker 返回非 *rabbitBroker")
	}
	if err := b.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := b.Connect(); err == nil {
		t.Fatal("拒连地址 Connect 应失败（本测试依赖连接失败后的半连接态）")
	}

	// amq.topic 是默认注册的 exchange，GetExchange 能过；下一道关卡正是
	// 半连接态下 nil 底层连接的 fail-fast。
	_, _, err := b.conn.Consume("amq.topic", "q.defect", "rk.defect", nil, nil, true, true, false)
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("半连接态 Consume = %v, want errors.Is(_, ErrNotConnected)", err)
	}
}

// TestNewRabbitChannelNilConnection 钉住 newRabbitChannel 源头的 nil 守卫：
// 所有调用点的半连接态都汇集到此处，必须在 (*amqp.Connection).Channel()
// 解引用之前 fail-fast，返回可 errors.Is 匹配的 ErrNotConnected。
func TestNewRabbitChannelNilConnection(t *testing.T) {
	ch, err := newRabbitChannel(nil, DefaultQos)
	if ch != nil {
		t.Fatalf("nil 连接应返回 nil channel, got %v", ch)
	}
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("nil 连接错误 = %v, want errors.Is(_, ErrNotConnected)", err)
	}
}
