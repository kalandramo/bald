package kafka

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/kafka"
	"github.com/kalandramo/bald/metrics"

	"github.com/kalandramo/bald/transport/subscribe"
)

type Server struct {
	broker.Broker
	brokerOpts []broker.Option
	m          metrics.Metrics

	subscribers    broker.SubscriberMap
	subscriberOpts subscribe.SubscribeOptionMap

	sync.RWMutex
	started atomic.Bool

	baseCtx context.Context
	err     error
}

func NewServer(opts ...ServerOption) *Server {
	srv := &Server{
		baseCtx:        context.Background(),
		subscribers:    make(broker.SubscriberMap),
		subscriberOpts: make(subscribe.SubscribeOptionMap),
		brokerOpts:     []broker.Option{},
		started:        atomic.Bool{},
	}

	srv.init(opts...)

	return srv
}

func (s *Server) init(opts ...ServerOption) {
	for _, o := range opts {
		o(s)
	}

	s.Broker = kafka.NewBroker(s.brokerOpts...)

}

func (s *Server) Name() string {
	return KindKafka
}

func (s *Server) Start(ctx context.Context) error {
	if s.err != nil {
		return s.err
	}

	if s.started.Load() {
		return nil
	}

	if s.err = s.Init(); s.err != nil {
		logErrorf("init broker failed: [%s]", s.err.Error())
		return s.err
	}

	if s.err = s.Connect(); s.err != nil {
		logErrorf("connect broker failed: [%s]", s.err.Error())
		return s.err
	}

	logInfof("server listening on: %s", s.Address())

	if s.err = s.doRegisterSubscriberMap(); s.err != nil {
		return s.err
	}

	s.baseCtx = ctx
	s.started.Store(true)

	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	if !s.started.Load() {
		return nil
	}

	logInfo("server stopping...")

	for _, v := range s.subscribers {
		_ = v.Unsubscribe(false)
	}
	s.subscribers = make(broker.SubscriberMap)
	s.subscriberOpts = make(subscribe.SubscribeOptionMap)

	s.started.Store(false)
	err := s.Disconnect()
	s.err = nil

	logInfo("server stopped.")

	return err
}

// RegisterSubscriber 注册一个订阅者
// @param ctx 上下文
// @param topic 订阅的主题
// @param queue 订阅的分组
// @param handler 订阅者的处理函数
func (s *Server) RegisterSubscriber(ctx context.Context, topic, queue string, disableAutoAck bool, handler broker.Handler, binder broker.Binder, opts ...broker.SubscribeOption) error {
	s.Lock()
	defer s.Unlock()

	//var subscribeOptions []broker.SubscribeOption
	opts = append(opts, broker.WithSubscribeQueueName(queue))
	if disableAutoAck {
		opts = append(opts, broker.WithSubscribeAutoAck(false))
	}

	// context必须要插入到头部，否则后续传入的配置会被覆盖掉。
	opts = append([]broker.SubscribeOption{broker.WithSubscribeContext(ctx)}, opts...)

	if s.started.Load() {
		return s.doRegisterSubscriber(topic, handler, binder, opts...)
	} else {
		s.subscriberOpts[topic] = &subscribe.SubscribeOption{Handler: handler, Binder: binder, SubscribeOptions: opts}
	}
	return nil
}

func RegisterSubscriber[T any](
	srv *Server,
	ctx context.Context,
	topic, queue string,
	disableAutoAck bool,
	handler broker.TypedHandler[T],
	opts ...broker.SubscribeOption,
) error {
	return srv.RegisterSubscriber(ctx,
		topic,
		queue,
		disableAutoAck,
		func(ctx context.Context, event broker.Event) error {
			switch t := event.Message().Body.(type) {
			case *T:
				if err := handler(ctx, event.Topic(), event.Message().Headers, t); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported type: %T", t)
			}
			return nil
		},
		func() any {
			var t T
			return &t
		},
		opts...,
	)
}

func (s *Server) doRegisterSubscriber(topic string, handler broker.Handler, binder broker.Binder, opts ...broker.SubscribeOption) error {
	handler = s.wrapHandler(topic, handler)

	sub, err := s.Subscribe(topic, handler, binder, opts...)
	if err != nil {
		return err
	}

	if old, exists := s.subscribers[topic]; exists {
		logWarnf("subscriber for topic '%s' already exists, overwriting", topic)
		// 旧订阅必须先摘除：只覆写映射的话，旧订阅还挂在 broker 上——
		// 消息双份投递且旧订阅 goroutine 泄漏（家族同构缺陷，段 2 redis 已修）。
		_ = old.Unsubscribe(false)
	}
	s.subscribers[topic] = sub
	return nil
}

func (s *Server) doRegisterSubscriberMap() error {
	var errs []error
	for topic, opt := range s.subscriberOpts {
		if err := s.doRegisterSubscriber(topic, opt.Handler, opt.Binder, opt.SubscribeOptions...); err != nil {
			logErrorf("register subscriber failed, topic: %s, error: %s", topic, err.Error())
			errs = append(errs, err)
			// 失败条目留在 subscriberOpts，下次 Start 重试（收敛语义）——此前
			// 无条件清空整批 deferred 队列：Start 失败后重试找不到待注册项，
			// 订阅静默丢失（家族同构缺陷，段 2 redis 已修，见传输层设计 §已知缺口 #2）。
			continue
		}
		delete(s.subscriberOpts, topic)
	}
	return errors.Join(errs...)
}

// Endpoint 是描述性端点（kafka://addr）——代理型传输不监听本地端口，
// 地址只在建连成功后才有意义：
//   - started 前返回 ""：appkit 的 waitForEndpoints 把 "" 判为「未就绪」，
//     而起动顺序保证 started=true 只在 Connect + 订阅注册全绿之后落；
//   - Stop 复位 started 后回到 ""（Stop 先 Store(false) 再 Disconnect）。
//
// 与 asynq 的 "asynq://" 前缀同形：带 scheme 的描述性端点会被
// registrableEndpoint 挡在服务注册之外，不影响发现。
func (s *Server) Endpoint() string {
	if !s.started.Load() {
		return ""
	}
	return "kafka://" + s.Address()
}

func (s *Server) wrapHandler(topic string, handler broker.Handler) broker.Handler {
	if s.m == nil {
		return handler
	}
	return func(ctx context.Context, event broker.Event) error {
		startTime := time.Now()
		labels := map[string]string{
			"broker": s.Name(),
			"topic":  topic,
		}

		s.m.Counter(ctx, "broker_messages_received_total", 1, labels)

		err := handler(ctx, event)
		s.m.Histogram(ctx, "broker_message_duration_seconds", time.Since(startTime).Seconds(), labels)

		if err != nil {
			s.m.Counter(ctx, "broker_messages_errors_total", 1, map[string]string{
				"broker": s.Name(),
				"topic":  topic,
				"error":  "true",
			})
		}

		return err
	}
}
