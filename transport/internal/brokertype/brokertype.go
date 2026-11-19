// Package brokertype 是 broker 型传输家族（kafka/rabbitmq/…）的共享 kernel：
// 服务器生命周期、订阅簿（deferred + live 双态）、metrics 包装、家族日志全部
// 只住这里——此前 kafka 与 rabbitmq 两份 ~84% 复制的 server.go 使 Stop 无锁
// 遍历等家族同构缺陷必然双发（架构诊断 C-4 实证），J2 的单一闸门收在本包。
//
// 家族差异收敛为 Family 的四个参数化点：Kind（Name/日志前缀）、KeyNoun
// （日志里订阅键的家族名词）、NewBroker（broker 工厂）、Endpoint（描述性
// 端点形态）。家族特有选项（kafka PLAIN/SCRAM、rabbitmq Exchange）与公共
// 注册签名（kafka 的 queue/disableAutoAck）留在各自 shell，壳以指针内嵌
// kernel 复用生命周期。
//
// 口径：映射突变一律持锁（Register/Stop/doRegisterSubscriberMap 三方对齐）；
// 失败条目留在 deferred 队列供下次 Start 重试（收敛语义）；nil ctx 回落
// baseCtx（家族统一取防御侧——此前 kafka 会把 nil 塞进订阅选项）。
package brokertype

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/metrics"

	"github.com/kalandramo/bald/transport/subscribe"
)

// Family 是一个 broker 型传输家族的参数化点。
type Family struct {
	// Kind 是 Name() 返回值与日志前缀（"[kafka]" / "[rabbitmq]"）。
	Kind string
	// KeyNoun 是日志里订阅键的家族名词（kafka 说 topic、rabbitmq 说
	// routingKey）——运维面保持各自领域的说法。
	KeyNoun string
	// NewBroker 由 shell 提供：shell 的 brokerOpts 收集完毕后经 BuildBroker
	// 调用，产出内嵌的 broker.Broker。
	NewBroker func(opts ...broker.Option) broker.Broker
	// Endpoint 把 broker 地址翻成描述性端点（kafka 加 scheme 前缀、
	// rabbitmq 剥 URL 凭证）。started 前的空串判据由 kernel 统一承担。
	Endpoint func(addr string) string
}

// Server 是家族共享的服务器内核。shell 以指针内嵌（*brokertype.Server）
// 复用生命周期方法并满足 transport.Server。
type Server struct {
	broker.Broker // 内嵌：Init/Connect/Address/Subscribe/Disconnect 由家族 broker 提供
	fam           Family

	brokerOpts []broker.Option
	m          metrics.Metrics

	subscribers    broker.SubscriberMap
	subscriberOpts subscribe.SubscribeOptionMap

	sync.RWMutex
	started atomic.Bool

	baseCtx context.Context
	err     error
}

// NewServer 构造内核：deferred/live 订阅簿、broker 选项收集面就绪。
// broker 本体由 shell 收集完选项后调 BuildBroker 落地。
func NewServer(fam Family) *Server {
	return &Server{
		fam:            fam,
		baseCtx:        context.Background(),
		subscribers:    make(broker.SubscriberMap),
		subscriberOpts: make(subscribe.SubscribeOptionMap),
		brokerOpts:     []broker.Option{},
		started:        atomic.Bool{},
	}
}

// AddBrokerOpt 追加一条家族 broker 选项（shell 的 With* 选项落点）。
func (s *Server) AddBrokerOpt(o broker.Option) {
	s.brokerOpts = append(s.brokerOpts, o)
}

// SetMetrics 注入指标面（nil = 不包装 handler）。
func (s *Server) SetMetrics(m metrics.Metrics) { s.m = m }

// BuildBroker 用已收集的 brokerOpts 构造家族 broker（shell NewServer 第二阶段）。
func (s *Server) BuildBroker() {
	s.Broker = s.fam.NewBroker(s.brokerOpts...)
}

// Name 满足 transport.Server。
func (s *Server) Name() string { return s.fam.Kind }

// Start 依次 Init/Connect/flush deferred 订阅，全绿才置 started。
func (s *Server) Start(ctx context.Context) error {
	if s.err != nil {
		return s.err
	}

	if s.started.Load() {
		return nil
	}

	if s.err = s.Init(); s.err != nil {
		s.logErrorf("init broker failed: [%s]", s.err.Error())
		return s.err
	}

	if s.err = s.Connect(); s.err != nil {
		s.logErrorf("connect broker failed: [%s]", s.err.Error())
		return s.err
	}

	s.logInfof("server listening on: %s", s.Address())

	if s.err = s.doRegisterSubscriberMap(); s.err != nil {
		return s.err
	}

	s.Lock()
	s.baseCtx = ctx
	s.Unlock()
	s.started.Store(true)

	return nil
}

// Stop 摘除全部订阅并断连；重复调用幂等（started 未置真即直返）。
func (s *Server) Stop(ctx context.Context) error {
	if !s.started.Load() {
		return nil
	}

	s.logInfo("server stopping...")

	// 映射突变必须持锁：Register 持 s.Lock() 写同一批映射，无锁遍历+重建在
	// 并发下是 concurrent map read/write fatal（家族同构缺陷，统一前在
	// kafka/rabbitmq 两份复制代码里双发，C-4 同源同修）。退订是订阅者自己的
	// 锁，放到锁外做，停机路径不为退订 I/O 长时间持有服务器锁。
	s.Lock()
	subs := make([]broker.Subscriber, 0, len(s.subscribers))
	for _, v := range s.subscribers {
		subs = append(subs, v)
	}
	s.subscribers = make(broker.SubscriberMap)
	s.subscriberOpts = make(subscribe.SubscribeOptionMap)
	// started 复位与映射清空同临界区完成：否则中间窗口里抢到锁的
	// Register 会按 started=true 注册出既不在退订快照里、又被随后
	// Disconnect 甩下的孤儿订阅。
	s.started.Store(false)
	s.Unlock()

	for _, v := range subs {
		_ = v.Unsubscribe(false)
	}

	err := s.Disconnect()
	s.err = nil

	s.logInfo("server stopped.")

	return err
}

// Register 是订阅注册核心：锁内完成 deferred/live 双态分流。shell 的公共
// RegisterSubscriber（家族各自的参数面）把家族特有选项追加完后落到这里。
func (s *Server) Register(ctx context.Context, key string, handler broker.Handler, binder broker.Binder, opts ...broker.SubscribeOption) error {
	s.Lock()
	defer s.Unlock()

	// 防御性兜底：字面量构造（baseCtx=nil）或调用方传 nil ctx 时回落
	// Background——nil ctx 塞进订阅选项会在投递路径炸出无诊断的 panic。
	if s.baseCtx == nil {
		s.baseCtx = context.Background()
	}
	if ctx == nil {
		ctx = s.baseCtx
	}

	// context必须要插入到头部，否则后续传入的配置会被覆盖掉。
	opts = append([]broker.SubscribeOption{broker.WithSubscribeContext(ctx)}, opts...)

	if s.started.Load() {
		return s.doRegisterSubscriber(key, handler, binder, opts...)
	}
	s.subscriberOpts[key] = &subscribe.SubscribeOption{Handler: handler, Binder: binder, SubscribeOptions: opts}
	return nil
}

func (s *Server) doRegisterSubscriber(key string, handler broker.Handler, binder broker.Binder, opts ...broker.SubscribeOption) error {
	handler = s.wrapHandler(key, handler)

	sub, err := s.Subscribe(key, handler, binder, opts...)
	if err != nil {
		return err
	}

	if old, exists := s.subscribers[key]; exists {
		s.logWarnf("subscriber for %s '%s' already exists, overwriting", s.fam.KeyNoun, key)
		// 旧订阅必须先摘除：只覆写映射的话，旧订阅还挂在 broker 上——
		// 消息双份投递且旧订阅 goroutine 泄漏（家族同构缺陷，段 2 redis 已修）。
		_ = old.Unsubscribe(false)
	}
	s.subscribers[key] = sub
	return nil
}

func (s *Server) doRegisterSubscriberMap() error {
	// Start 阶段的延迟注册与并发的 Register 写同一批映射：遍历+删除同样要
	// 持锁（started 路径的 Register 本就持锁调 doRegisterSubscriber，此处与
	// 其对齐；订阅者的 Unsubscribe 不碰服务器锁，无重入风险）。
	s.Lock()
	defer s.Unlock()

	var errs []error
	for key, opt := range s.subscriberOpts {
		if err := s.doRegisterSubscriber(key, opt.Handler, opt.Binder, opt.SubscribeOptions...); err != nil {
			s.logErrorf("register subscriber failed, %s: %s, error: %s", s.fam.KeyNoun, key, err.Error())
			errs = append(errs, err)
			// 失败条目留在 subscriberOpts，下次 Start 重试（收敛语义）——此前
			// 无条件清空整批 deferred 队列：Start 失败后重试找不到待注册项，
			// 订阅静默丢失（家族同构缺陷，段 2 redis 已修，见传输层设计 §已知缺口 #2）。
			continue
		}
		delete(s.subscriberOpts, key)
	}
	return errors.Join(errs...)
}

// Endpoint 是描述性端点——代理型传输不监听本地端口，地址只在建连成功后
// 才有意义：
//   - started 前返回 ""：appkit 的 waitForEndpoints 把 "" 判为「未就绪」，
//     而起动顺序保证 started=true 只在 Connect + 订阅注册全绿之后落；
//   - Stop 复位 started 后回到 ""（Stop 先 Store(false) 再 Disconnect）。
//
// 具体形态（scheme 前缀 / 凭证剥离）由 fam.Endpoint 翻译。
func (s *Server) Endpoint() string {
	if !s.started.Load() {
		return ""
	}
	return s.fam.Endpoint(s.Address())
}

func (s *Server) wrapHandler(key string, handler broker.Handler) broker.Handler {
	if s.m == nil {
		return handler
	}
	return func(ctx context.Context, event broker.Event) error {
		startTime := time.Now()
		labels := map[string]string{
			"broker": s.Name(),
			"topic":  key,
		}

		s.m.Counter(ctx, "broker_messages_received_total", 1, labels)

		err := handler(ctx, event)
		s.m.Histogram(ctx, "broker_message_duration_seconds", time.Since(startTime).Seconds(), labels)

		if err != nil {
			s.m.Counter(ctx, "broker_messages_errors_total", 1, map[string]string{
				"broker": s.Name(),
				"topic":  key,
				"error":  "true",
			})
		}

		return err
	}
}

// ---- 家族日志（实例级前缀：同进程双家族共存时包级 logKey 不成立）----

func (s *Server) logInfo(args ...any) {
	log.Info(nil, fmt.Sprintf("[%s] %s", s.fam.Kind, fmt.Sprint(args...)))
}

func (s *Server) logError(args ...any) {
	log.Error(nil, fmt.Sprintf("[%s] %s", s.fam.Kind, fmt.Sprint(args...)))
}

func (s *Server) logInfof(format string, args ...any) {
	log.Info(nil, fmt.Sprintf("[%s] %s", s.fam.Kind, fmt.Sprintf(format, args...)))
}

func (s *Server) logWarnf(format string, args ...any) {
	log.Warn(nil, fmt.Sprintf("[%s] %s", s.fam.Kind, fmt.Sprintf(format, args...)))
}

func (s *Server) logErrorf(format string, args ...any) {
	log.Error(nil, fmt.Sprintf("[%s] %s", s.fam.Kind, fmt.Sprintf(format, args...)))
}

// RegisterTypedSubscriber 是两家族壳的泛型订阅糖上收（片 3）：此前
// RegisterSubscriber[T] 的类型断言包装在 kafka/rabbitmq 各一份逐行克隆。
// 家族壳保留一行委托（保持各自 API 形状），逻辑单份住这里。
func RegisterTypedSubscriber[T any](srv *Server, ctx context.Context, topic, queue string, disableAutoAck bool, handler broker.TypedHandler[T], opts ...broker.SubscribeOption) error {
	if disableAutoAck {
		opts = append(opts, broker.WithSubscribeAutoAck(false))
	}
	return srv.Register(ctx, topic, broker.Handler(func(ctx context.Context, event broker.Event) error {
		switch t := event.Message().Body.(type) {
		case *T:
			return handler(ctx, event.Topic(), event.Message().Headers, t)
		default:
			return fmt.Errorf("unsupported type: %T", t)
		}
	}), func() any {
		var t T
		return &t
	}, append(opts,
		broker.WithSubscribeQueueName(queue),
	)...)
}

// RegisterUntypedSubscriber 是非泛型形态的上收：disableAutoAck 时关自动确认。
func RegisterUntypedSubscriber(srv *Server, ctx context.Context, topic, queue string, disableAutoAck bool, handler broker.Handler, binder broker.Binder, opts ...broker.SubscribeOption) error {
	if disableAutoAck {
		opts = append(opts, broker.WithSubscribeAutoAck(false))
	}
	return srv.Register(ctx, topic, handler, binder, opts...)
}
