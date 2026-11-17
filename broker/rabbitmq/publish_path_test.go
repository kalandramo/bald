package rabbitmq

import (
	"context"
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

// TestUnconnectedBrokerPublishPanics_KnownDefect 复现并钉住一个已知产品缺陷
// （本次不修，仅记录证据）：broker.Connect 拨号失败后 b.conn 已非 nil 但底层
// Connection 为 nil；此后 Publish 经 lazyInitPublishChannel → newRabbitChannel(nil)
// → (*amqp.Connection).Channel() nil 解引用 panic。若将来修复，请把本测试改为
// 断言「如实返回错误而非 panic」。
func TestUnconnectedBrokerPublishPanics_KnownDefect(t *testing.T) {
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
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("当前实现下未连接 Publish 应 panic（已知缺陷）；若已修复请更新本测试")
		}
	}()
	_ = b.Publish(context.Background(), "rk.defect", &broker.Message{Body: "x"})
}

// ---- newRabbitChannel：nil 连接会 panic，离线不可达，显式跳过并说明 ----

func TestNewRabbitChannelRequiresRealConnection(t *testing.T) {
	t.Skip("newRabbitChannel 内部调用 conn.Channel()，nil 零值连接会 panic；" +
		"真实握手属集成测试范围，同仓 transport/rabbitmq 已确立『假后端不可信』的判断")
}
