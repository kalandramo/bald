package kafka

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"sync"
	"time"

	kafkaGo "github.com/segmentio/kafka-go"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/retry"
)

const (
	defaultDeadlineMin = 50 * time.Millisecond
	defaultDeadlineMax = 500 * time.Millisecond
	defaultErrorMin    = 500 * time.Millisecond
	defaultErrorMax    = 5 * time.Second
	defaultMaxAttempts = 6
)

type subscriber struct {
	sync.RWMutex

	b *kafkaBroker

	topic string

	options broker.SubscribeOptions
	handler broker.Handler
	binder  broker.Binder

	reader *kafkaGo.Reader

	closed bool
	done   chan struct{}

	batchSize     int
	batchInterval time.Duration

	// 退避配置
	deadlineMin time.Duration
	deadlineMax time.Duration
	errorMin    time.Duration
	errorMax    time.Duration
	maxAttempts int
}

func newSubscriber(
	b *kafkaBroker,
	topic string,
	options broker.SubscribeOptions,
	readerConfig kafkaGo.ReaderConfig,
	handler broker.Handler,
	binder broker.Binder,
) *subscriber {
	sub := &subscriber{
		b:       b,
		options: options,
		topic:   topic,
		handler: handler,
		binder:  binder,
		reader:  kafkaGo.NewReader(readerConfig),
		done:    make(chan struct{}),

		deadlineMin: defaultDeadlineMin,
		deadlineMax: defaultDeadlineMax,
		errorMin:    defaultErrorMin,
		errorMax:    defaultErrorMax,
		maxAttempts: defaultMaxAttempts,
	}
	return sub
}

func (s *subscriber) Options() broker.SubscribeOptions {
	s.RLock()
	defer s.RUnlock()

	return s.options
}

func (s *subscriber) Topic() string {
	s.RLock()
	defer s.RUnlock()

	return s.topic
}

func (s *subscriber) Unsubscribe(removeFromManager bool) error {
	s.Lock()
	err := s.closeLocked()
	s.Unlock()

	if s.b != nil && s.b.subscribers != nil && removeFromManager {
		_ = s.b.subscribers.RemoveOnly(s.topic)
	}

	return err
}

func (s *subscriber) close() error {
	s.Lock()
	defer s.Unlock()
	return s.closeLocked()
}

func (s *subscriber) closeLocked() error {
	if s.closed {
		return nil
	}

	s.closed = true

	var err error
	if s.reader != nil {
		err = s.reader.Close()
	}

	if s.done != nil {
		close(s.done)
	}

	// done 保持关闭状态、不置 nil：run() 的 select 不加锁地读这个字段，
	// 事后改写它就是数据竞争；而且「永远就绪」正是循环需要退出信号。
	// 二次 close 由上面的 closed 标志挡住。
	return err
}

func (s *subscriber) IsClosed() bool {
	s.RLock()
	defer s.RUnlock()

	return s.closed
}

func (s *subscriber) isBatchMode() bool {
	return s.batchSize > 0 || s.batchInterval > 0
}

func (s *subscriber) run() {
	if s.isBatchMode() {
		s.processBatchMessage()
	} else {
		s.processSingleMessage()
	}
}

func (s *subscriber) processBatchMessage() {
	messageBuffer := make([]kafkaGo.Message, 0, s.batchSize)

	ticker := time.NewTicker(s.batchInterval)
	defer ticker.Stop()

	// 退避参数
	deadlineAttempt := 0
	errorAttempt := 0

	for {
		select {
		case <-s.options.Context.Done():
			_ = s.close()
			return

		case <-s.done:
			_ = s.close()
			return

		case <-ticker.C:
			// 定时触发批量处理
			if len(messageBuffer) > 0 {
				s.handleBatchMessage(messageBuffer)
				messageBuffer = make([]kafkaGo.Message, 0, s.batchSize)
			}

		default:
			// 设置读取超时，避免阻塞时间过长
			ctx, cancel := context.WithTimeout(s.options.Context, 1*time.Second)
			m, err := s.reader.FetchMessage(ctx)
			cancel()

			if err != nil {
				// 先判断是否需要立即退出
				if isFatalError(s, err) {
					logErrorf("fatal FetchMessage error, exiting: %v", err)
					_ = s.close()
					return
				}

				if errors.Is(err, context.DeadlineExceeded) {
					// 对短超时使用较小的退避并带抖动
					backoffSleep(deadlineAttempt, s.deadlineMin, s.deadlineMax)
					if deadlineAttempt < 6 {
						deadlineAttempt++
					}
					continue
				}

				logErrorf("FetchMessage error: %s", err.Error())
				backoffSleep(errorAttempt, s.errorMin, s.errorMax)
				if errorAttempt < 6 {
					errorAttempt++
				}
				continue
			}

			// 成功读取，重置退避计数
			deadlineAttempt = 0
			errorAttempt = 0

			// 将消息添加到缓冲区
			messageBuffer = append(messageBuffer, m)

			// 当缓冲区达到批处理大小时处理消息
			if len(messageBuffer) >= s.batchSize {
				s.handleBatchMessage(messageBuffer)
				messageBuffer = make([]kafkaGo.Message, 0, s.batchSize)
			}
		}
	}
}

func (s *subscriber) processSingleMessage() {
	// 退避参数
	errorAttempt := 0

	for {
		select {
		case <-s.options.Context.Done():
			_ = s.close()
			return

		case <-s.done:
			_ = s.close()
			return

		default:
			km, err := s.reader.FetchMessage(s.options.Context)
			if err != nil {
				// 立即退出的情况
				if isFatalError(s, err) {
					logErrorf("fatal FetchMessage error, exiting: %v", err)
					_ = s.close()
					return
				}

				logErrorf("FetchMessage error: %s", err.Error())
				backoffSleep(errorAttempt, s.errorMin, s.errorMax)
				if errorAttempt < 6 {
					errorAttempt++
				}
				continue
			}

			// 成功读取，重置退避计数
			errorAttempt = 0

			_ = s.handleMessage(km)
		}
	}
}

func (s *subscriber) handleBatchMessage(messages []kafkaGo.Message) {
	for _, km := range messages {
		if done := s.handleMessage(km); done {
			logErrorf("[batch] handleMessage failed for topic %s partition %d offset %d", km.Topic, km.Partition, km.Offset)
		}
	}
}

func (s *subscriber) handleMessage(km kafkaGo.Message) bool {
	var err error

	// Use context.Background() as base to isolate each message's handling.
	ctx := context.Background()

	bm := &broker.Message{
		Headers:   kafkaHeaderToMap(km.Headers),
		Body:      nil,
		Partition: km.Partition,
		Offset:    km.Offset,
	}

	if s.binder != nil {
		bm.Body = s.binder()

		if err = broker.Unmarshal(s.b.options.Codec, km.Value, &bm.Body); err != nil {
			logErrorf("unmarshal message failed: %v", err)
			return true
		}
	} else {
		bm.Body = km.Value
	}

	pub := newPublication(ctx, s.reader, km, bm)

	if err = s.handler(ctx, pub); err != nil {
		logErrorf("handle message failed: %v", err)
		return true
	}

	if s.options.AutoAck {
		if err = pub.Ack(); err != nil {
			logErrorf("unable to commit km: %v", err)
			return true
		}
	}

	return false
}

// backoffDuration 计算第 attempt 次退避的时长：指数增长（2 为基数）截到 max，
// 再叠加 [d/2, d] 的抖动。曲线与抖动都取 [retry] 的实现，本函数只负责
// kafka 侧的两条自兜底：
//   - min<=0 用 50ms（retry 的零值兜底目标是 200ms，这里保持订阅侧原口径）；
//   - max<min 抬到 min，避免出现「上界小于下界」的空区间。
//
// 拆成纯函数（rng 注入）是为了能确定性断言边界，不必拿墙上时钟去猜。
func backoffDuration(attempt int, min, max time.Duration, rng func() float64) time.Duration {
	if min <= 0 {
		min = 50 * time.Millisecond
	}
	if max < min {
		max = min
	}
	exp := retry.ExponentialBackoff{Initial: min, Factor: 2, Max: max}
	return retry.EqualJitter(exp.Delay(attempt), rng)
}

// backoffSleep 按 backoffDuration 睡一觉。抖动取 math/rand 的全局函数：每个订阅
// 各自跑在一个 goroutine 上，这里会并发调用，而 rand.New(rand.NewSource(..))
// 得到的自建源不是并发安全的。
func backoffSleep(attempt int, min, max time.Duration) {
	time.Sleep(backoffDuration(attempt, min, max, rand.Float64))
}

// helper: 判断是否为不可恢复/致命错误，需立即退出
func isFatalError(s *subscriber, err error) bool {
	if err == nil {
		return false
	}
	// 如果外部上下文已被取消/截止，立即退出
	if errors.Is(err, context.Canceled) || (s != nil && s.options.Context != nil && s.options.Context.Err() != nil) {
		return true
	}
	// 如果订阅者已被关闭，退出
	if s != nil && s.IsClosed() {
		return true
	}
	// 非超时的 net.Error 视为致命（例如永久连接失败）；超时按可重试处理。
	var ne net.Error
	if errors.As(err, &ne) && !ne.Timeout() {
		return true
	}
	// io.EOF 通常是连接断开（broker 重启、rebalance、网络波动等），
	// kafka-go Reader 内部会自动重建连接，只需退避重试即可，不应退出。
	// 其余错误（包括 EOF）均视为可重试的临时错误。
	return false
}
