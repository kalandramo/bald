package pubsub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/kalandramo/bald/broker"
	redisOption "github.com/kalandramo/bald/broker/redis/option"
)

const (
	defaultBroker = "redis://127.0.0.1:6379"
)

type pubsubBroker struct {
	addr       string
	pool       *redis.Pool
	options    broker.Options
	commonOpts *redisOption.CommonOptions

	subscribers *broker.SubscriberSyncMap
}

// NewBroker 构造 pubsub broker（**尚未 Init，addr 为空**）。
//
// **生命周期约定**：构造后必须依次调用 `Init()`（解析 addr）与 `Connect()`
// （建连）才能收发消息——直接 `Connect()` 会因 addr 为空而报
// `invalid redis URL scheme:`（错误信息具误导性：scheme 明明给了，实际是
// addr 未初始化）。这与框架内其他 broker（kafka/rabbitmq/rocketmq）一致，
// 各 contract 的 Provider 都是 NewBroker → Init → Connect 三步。
// 见框架缺陷报告 D13.1。
func NewBroker(opts ...broker.Option) broker.Broker {
	commonOpts := &redisOption.CommonOptions{
		MaxIdle:        redisOption.DefaultMaxIdle,
		MaxActive:      redisOption.DefaultMaxActive,
		IdleTimeout:    redisOption.DefaultIdleTimeout,
		ConnectTimeout: redisOption.DefaultConnectTimeout,
		ReadTimeout:    redisOption.DefaultReadTimeout,
		WriteTimeout:   redisOption.DefaultWriteTimeout,
	}

	options := broker.NewOptionsAndApply(opts...)

	return &pubsubBroker{
		options:     options,
		commonOpts:  commonOpts,
		subscribers: broker.NewSubscriberSyncMap(),
	}
}

func (b *pubsubBroker) Name() string {
	return "redis"
}

func (b *pubsubBroker) Options() broker.Options {
	return b.options
}

func (b *pubsubBroker) Address() string {
	return b.addr
}

func (b *pubsubBroker) Init(opts ...broker.Option) error {
	if b.pool != nil {
		return errors.New("redis: cannot init while connected")
	}

	var addr string

	if len(b.options.Addrs) == 0 || b.options.Addrs[0] == "" {
		addr = defaultBroker
	} else {
		addr = b.options.Addrs[0]

		if !strings.HasPrefix(addr, "redis://") {
			addr = "redis://" + addr
		}
	}

	b.addr = addr

	b.options.Apply(opts...)

	if v, ok := b.options.Context.Value(redisOption.OptionsKey).(*redisOption.CommonOptions); ok {
		b.commonOpts = v
	}

	return nil
}

func (b *pubsubBroker) Connect() error {
	if b.pool != nil {
		return nil
	}

	b.pool = &redis.Pool{
		MaxIdle:     b.commonOpts.MaxIdle,
		MaxActive:   b.commonOpts.MaxActive,
		IdleTimeout: b.commonOpts.IdleTimeout,
		Dial: func() (redis.Conn, error) {
			return redis.DialURL(
				b.addr,
				redis.DialConnectTimeout(b.commonOpts.ConnectTimeout),
				redis.DialReadTimeout(redisOption.DefaultHealthCheckPeriod+b.commonOpts.ReadTimeout),
				redis.DialWriteTimeout(b.commonOpts.WriteTimeout),
			)
		},
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			if nil != err {
				redisOption.LogError("ping error:" + err.Error())
			}
			return err
		},
	}

	// 探活（D13.2 修复，2026-09-22）：redigo 的 Pool 是**懒连接**——上面的
	// Dial 只在首次 pool.Get() 时才真正建连。故此处若不探活，Redis 不可达时
	// 本方法会返回 nil（**假成功**），故障被推迟到首次 Publish/Subscribe 才
	// 暴露，误导启动期健康检查。这里取一条连接并 PING，把「连不上」提前到
	// 本方法的返回值。
	conn := b.pool.Get()
	defer conn.Close()
	if _, err := conn.Do("PING"); err != nil {
		// 探活失败：丢弃 pool（下次 Connect 重建），返回错误。
		_ = b.pool.Close()
		b.pool = nil
		return fmt.Errorf("redis: connect: %w", err)
	}

	return nil
}

func (b *pubsubBroker) Disconnect() error {
	err := b.pool.Close()
	b.pool = nil
	b.addr = ""

	b.subscribers.Clear()

	return err
}

func (b *pubsubBroker) Publish(ctx context.Context, topic string, msg *broker.Message, opts ...broker.PublishOption) error {
	var finalTask = b.internalPublish

	if len(b.options.PublishMiddlewares) > 0 {
		finalTask = broker.ChainPublishMiddleware(finalTask, b.options.PublishMiddlewares)
	}

	return finalTask(ctx, topic, msg, opts...)
}

func (b *pubsubBroker) internalPublish(ctx context.Context, topic string, msg *broker.Message, opts ...broker.PublishOption) error {
	buf, err := broker.Marshal(b.options.Codec, msg.Body)
	if err != nil {
		return err
	}

	sendMsg := msg.Clone()
	sendMsg.Body = buf

	return b.publish(ctx, topic, sendMsg, opts...)
}

func (b *pubsubBroker) publish(_ context.Context, topic string, msg *broker.Message, _ ...broker.PublishOption) error {
	conn := b.pool.Get()
	_, err := redis.Int(conn.Do("PUBLISH", topic, msg.BodyBytes()))
	_ = conn.Close()
	return err
}

func (b *pubsubBroker) Subscribe(topic string, handler broker.Handler, binder broker.Binder, opts ...broker.SubscribeOption) (broker.Subscriber, error) {
	options := broker.SubscribeOptions{
		Context: context.Background(),
	}
	for _, o := range opts {
		o(&options)
	}

	if len(b.options.SubscriberMiddlewares) > 0 {
		handler = broker.ChainSubscriberMiddleware(handler, b.options.SubscriberMiddlewares)
	}

	sub := &subscriber{
		b:       b,
		conn:    &redis.PubSubConn{Conn: b.pool.Get()},
		topic:   topic,
		handler: handler,
		binder:  binder,
		options: options,
	}

	if err := sub.conn.Subscribe(sub.topic); err != nil {
		_ = sub.conn.Close()
		return nil, err
	}

	b.subscribers.Add(topic, sub)

	go sub.recv()

	return sub, nil
}
