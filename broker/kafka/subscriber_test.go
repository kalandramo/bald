package kafka

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	kafkaGo "github.com/segmentio/kafka-go"

	"github.com/kalandramo/bald/broker"
)

// fixedRand 把抖动变成可断言的确定值，同时记下传给它的上界，
// 于是「指数增长 + 截到 max + [d/2, d) 抖动」这条规则能逐边界核对。
type fixedRand struct {
	mu     sync.Mutex
	nCalls []int64
	fn     func(n int64) int64
}

func newZeroRand() *fixedRand {
	return &fixedRand{fn: func(int64) int64 { return 0 }}
}

func (f *fixedRand) int63n(n int64) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nCalls = append(f.nCalls, n)
	return f.fn(n)
}

func (f *fixedRand) lastN() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.nCalls) == 0 {
		return -1
	}
	return f.nCalls[len(f.nCalls)-1]
}

func TestBackoffDuration(t *testing.T) {
	const (
		min50  = 50 * time.Millisecond
		max500 = 500 * time.Millisecond
	)

	full := &fixedRand{fn: func(n int64) int64 { return n - 1 }}

	cases := []struct {
		name string
		// wantD 是抖动前的目标时长（指数增长后截到 max）
		attempt int
		min     time.Duration
		max     time.Duration
		wantD   time.Duration
	}{
		{"attempt 0", 0, min50, max500, min50},
		{"attempt 1", 1, min50, max500, 100 * time.Millisecond},
		{"attempt 3", 3, min50, max500, 400 * time.Millisecond},
		// 400ms*2 越过 max，必须截在 500ms
		{"attempt 4 截到 max", 4, min50, max500, max500},
		{"attempt 6 截到 max", 6, min50, max500, max500},
		// min<<attempt 溢出成 0 时回到 min。run() 里计数封顶在 6，
		// 但退避函数不该依赖调用方记得封顶。
		{"attempt 64 位移溢出", 64, min50, max500, min50},
		{"attempt 100 位移溢出", 100, min50, max500, min50},
		// 非法配置自兜底：min<=0 用 50ms
		{"min 为零", 0, 0, max500, min50},
		{"min 为负", 0, -1, max500, min50},
		// max<min 时抬到 min，不产生「上界小于下界」的空区间
		{"max 小于 min", 0, 200 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 零抖动 = 区间下界 d/2
			r := newZeroRand()
			if got := backoffDuration(c.attempt, c.min, c.max, r.int63n); got != c.wantD/2 {
				t.Fatalf("零抖动: backoffDuration(%d, %v, %v) = %v, want %v",
					c.attempt, c.min, c.max, got, c.wantD/2)
			}
			// 随机数取上界最大值 d/2 时落在 d，所以区间是 [d/2, d] 双闭
			if got := backoffDuration(c.attempt, c.min, c.max, full.int63n); got != c.wantD {
				t.Fatalf("满抖动: got %v, want %v", got, c.wantD)
			}
			if n := r.lastN(); n != int64(c.wantD/2)+1 {
				t.Fatalf("抖动上界参数 = %d, want %d（rand63n 的入参）", n, int64(c.wantD/2)+1)
			}
		})
	}
}

// TestBackoffDuration_StaysInJitterBand 扫一遍 attempt/配置组合，
// 确认退避始终落在 [d/2, d]，d 为截断后的目标时长（max<min 时上界抬到 min）。
func TestBackoffDuration_StaysInJitterBand(t *testing.T) {
	mid := (&fixedRand{fn: func(n int64) int64 { return n / 2 }}).int63n

	for attempt := 0; attempt < 10; attempt++ {
		for _, min := range []time.Duration{50 * time.Millisecond, time.Second} {
			for _, max := range []time.Duration{500 * time.Millisecond, 5 * time.Second} {
				// 与实现同口径的自兜底，用来算期望区间
				effMin := min
				effMax := max
				if effMax < effMin {
					effMax = effMin
				}
				d := min << uint(attempt)
				if d < effMin {
					d = effMin
				}
				if d > effMax {
					d = effMax
				}

				got := backoffDuration(attempt, min, max, mid)
				if got < d/2 || got > d {
					t.Fatalf("attempt=%d min=%v max=%v: 退避 %v 不在 [%v, %v] 内",
						attempt, min, max, got, d/2, d)
				}
			}
		}
	}
}

// TestBackoffSleep_ConcurrentSafe 是这条改动的回归位：退避原先用
// rand.New(rand.NewSource(..)) 的自建源，而每个订阅各自跑在一个 goroutine 上，
// 并发命中同一个源是数据竞争。现在走 math/rand 的全局函数。
func TestBackoffSleep_ConcurrentSafe(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 5; attempt++ {
				backoffSleep(attempt, time.Millisecond, 2*time.Millisecond)
			}
		}()
	}
	wg.Wait()
}

// timeoutNetError 只为凑出 net.Error 接口里的 Timeout() == true。
type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

var _ net.Error = timeoutNetError{}

func TestIsFatalError(t *testing.T) {
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// isFatalError 只读订阅者的上下文与关闭位，不碰 reader，所以这些
	// reader 指向的地址永远不会联网。
	live := newTestSubscriber(t, "127.0.0.1:0")

	canceled := newSubscriber(nil, "t", broker.SubscribeOptions{Context: canceledCtx},
		kafkaGo.ReaderConfig{Brokers: []string{"127.0.0.1:0"}, Topic: "bald.fatal.test"}, noopHandler, nil)

	closed := newTestSubscriber(t, "127.0.0.1:0")
	if err := closed.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	cases := []struct {
		name string
		sub  *subscriber
		err  error
		want bool
	}{
		{"nil 错误不致命", live, nil, false},
		{"context.Canceled 致命", live, context.Canceled, true},
		{"包装的 Canceled 致命", live, fmt.Errorf("fetch: %w", context.Canceled), true},
		// 订阅自己的上下文已失效：任何错误都该立刻停止重试
		{"订阅上下文已取消", canceled, errors.New("whatever"), true},
		{"订阅者已关闭", closed, errors.New("whatever"), true},
		// 超时是网络抖动，交给退避重试
		{"net 超时不致命", live, timeoutNetError{}, false},
		{"context 超时不致命", live, context.DeadlineExceeded, false},
		// 非超时的 net.Error（连不上、被拒）判致命，避免无限退避
		{"net 拒绝连接致命", live, &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		// io.EOF 是 broker 重启/rebalance 的正常现象，kafka-go 自己会重连
		{"EOF 不致命", live, io.EOF, false},
		{"普通错误不致命", live, errors.New("temporary"), false},
		{"nil 订阅者不 panic", nil, errors.New("boom"), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isFatalError(c.sub, c.err); got != c.want {
				t.Fatalf("isFatalError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// newTestSubscriber 造一个指向 addr 的订阅者，不启动 goroutine，
// 让 run() 在测试自己的控制下调用。上下文由 t.Cleanup 负责取消。
func newTestSubscriber(t *testing.T, addr string) *subscriber {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	return newTestSubscriberCtx(t, addr, ctx)
}

// newTestSubscriberCtx 同上，但把上下文交给调用方握着（取消路径要自己触发）。
func newTestSubscriberCtx(t *testing.T, addr string, ctx context.Context) *subscriber {
	t.Helper()

	readerConfig := kafkaGo.ReaderConfig{
		Brokers:        []string{addr},
		Topic:          "bald.sub.test",
		GroupID:        "bald-sub-test",
		MaxAttempts:    1,
		MaxWait:        10 * time.Millisecond,
		ReadBackoffMin: time.Millisecond,
		ReadBackoffMax: 5 * time.Millisecond,
		StartOffset:    kafkaGo.FirstOffset,
		Dialer:         &kafkaGo.Dialer{Timeout: 500 * time.Millisecond},
	}

	options := broker.SubscribeOptions{Context: ctx, AutoAck: true}

	return newSubscriber(nil, readerConfig.Topic, options, readerConfig, noopHandler, nil)
}

func noopHandler(context.Context, broker.Event) error { return nil }

// TestSubscriberRun_ExitsOnFatalFetchError 钉住致命错误的退出路径：
// 指向无人监听的地址时 FetchMessage 返回非超时 net.Error，run() 必须终止，
// 而不是永远退避重试。退出瞬间订阅上下文仍然有效，所以「退出」只能归因于
// 致命判定，不可能是上下文取消。
func TestSubscriberRun_ExitsOnFatalFetchError(t *testing.T) {
	s := newTestSubscriber(t, refusedAddr(t))

	exited := make(chan struct{})
	go func() {
		s.run()
		close(exited)
	}()

	select {
	case <-exited:
	case <-time.After(15 * time.Second):
		t.Fatal("致命 fetch 错误下 run() 没有退出")
	}

	if s.options.Context.Err() != nil {
		t.Fatal("run() 的退出不应归因于上下文取消")
	}
	if !s.IsClosed() {
		t.Fatal("致命错误退出时应已释放 reader 与 done")
	}
}

// TestSubscriberRun_ExitsOnContextCancel 覆盖另一条退出路径：上下文已失效时
// run() 收尾并 close。这里在调用前就取消，select 的第一支确定性命中，
// 不会和致命错误路径抢（同一个订阅指向死地址时两条路都能退出）。
func TestSubscriberRun_ExitsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newTestSubscriberCtx(t, refusedAddr(t), ctx)
	cancel()

	done := make(chan struct{})
	go func() {
		s.run()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("上下文已取消，run() 仍未退出")
	}
	if !s.IsClosed() {
		t.Fatal("取消退出后应已释放 reader 与 done")
	}
}

// TestSubscriberClose_Idempotent 第二次 close 不能再 close 已关的 done chan，
// 否则 panic；Unsubscribe 与上下文取消两条路径会并发命中同一个订阅者。
func TestSubscriberClose_Idempotent(t *testing.T) {
	s := newTestSubscriber(t, refusedAddr(t))

	if err := s.close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := s.close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := s.Unsubscribe(false); err != nil {
		t.Fatalf("Unsubscribe after close: %v", err)
	}
	if !s.IsClosed() {
		t.Fatal("IsClosed 应为 true")
	}
	// done 关一次后必须保持「已关闭且不再被改写」：run() 的 select 不加锁读它。
	select {
	case <-s.done:
	default:
		t.Fatal("close 后 done 应已关闭")
	}
}

// TestSubscriberUnsubscribe_RemovesFromBroker 走 removeFromManager=true：
// 订阅者要从 broker 的注册表里摘掉，否则 Disconnect 时对着已关订阅再清一次。
func TestSubscriberUnsubscribe_RemovesFromBroker(t *testing.T) {
	b, ok := NewBroker(broker.WithAddress(refusedAddr(t)), WithEnableErrorLogger(false)).(*kafkaBroker)
	if !ok {
		t.Fatal("NewBroker 返回了非 *kafkaBroker")
	}
	if err := b.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := b.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = b.Disconnect() })

	const topic = "bald.sub.unlink"
	sub, err := b.Subscribe(topic, noopHandler, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if !b.subscribers.Has(topic) {
		t.Fatal("Subscribe 后注册表里应有该 topic")
	}

	if err := sub.Unsubscribe(true); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	if b.subscribers.Has(topic) {
		t.Fatal("Unsubscribe(true) 后注册表应已摘除该 topic")
	}
}
