package stream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/kalandramo/bald/broker"
	redisOption "github.com/kalandramo/bald/broker/redis/option"
)

const (
	defaultBroker = "redis://127.0.0.1:6379"
)

type streamBroker struct {
	// mu 守护 pool 的读写：Disconnect（停机 goroutine）写 nil 与订阅 recv
	// goroutine 读 pool 并发——race detector 实锤（段 5），加锁前停机路径
	// 必然与在途订阅循环竞态。
	mu         sync.RWMutex
	addr       string
	pool       *redis.Pool
	options    broker.Options
	commonOpts *redisOption.CommonOptions

	subscribers *broker.SubscriberSyncMap
}

// NewBroker 构造 stream broker（**尚未 Init，addr 为空**）。
//
// 生命周期同 pubsub driver：必须 NewBroker → Init → Connect 三步
// （直接 Connect 会因 addr 为空报误导性的 `invalid redis URL scheme:`）。
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

	return &streamBroker{
		options:     options,
		commonOpts:  commonOpts,
		subscribers: broker.NewSubscriberSyncMap(),
	}
}

func (b *streamBroker) Name() string {
	return "redis-stream"
}

func (b *streamBroker) Options() broker.Options {
	return b.options
}

func (b *streamBroker) Address() string {
	return b.addr
}

func (b *streamBroker) Init(opts ...broker.Option) error {
	if b.pool != nil {
		return errors.New("redis-stream: cannot init while connected")
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

func (b *streamBroker) Connect() error {
	b.mu.Lock()
	defer b.mu.Unlock()
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

	// redigo 的 Pool 是懒连接：不在这里探活的话，Redis 不可达时本方法会
	// 返回 nil（假成功），故障推迟到首次 Publish/Subscribe 才暴露。取一条
	// 连接 PING，把失败提前到返回值。
	conn := b.pool.Get()
	defer func() { _ = conn.Close() }()
	if _, err := conn.Do("PING"); err != nil {
		_ = b.pool.Close()
		b.pool = nil
		return fmt.Errorf("redis: connect: %w", err)
	}

	return nil
}

func (b *streamBroker) Disconnect() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pool == nil {
		return nil
	}
	err := b.pool.Close()
	b.pool = nil
	b.addr = ""

	b.subscribers.Clear()

	return err
}

// getPool 是 pool 的并发安全读取口：订阅 recv goroutine 与 Disconnect 并发，
// 直读字段会被 race detector 判红（段 5 实测）。
func (b *streamBroker) getPool() *redis.Pool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.pool
}

func (b *streamBroker) Publish(ctx context.Context, topic string, msg *broker.Message, opts ...broker.PublishOption) error {
	var finalTask = b.internalPublish

	if len(b.options.PublishMiddlewares) > 0 {
		finalTask = broker.ChainPublishMiddleware(finalTask, b.options.PublishMiddlewares)
	}

	return finalTask(ctx, topic, msg, opts...)
}

func (b *streamBroker) internalPublish(ctx context.Context, topic string, msg *broker.Message, opts ...broker.PublishOption) error {
	buf, err := broker.Marshal(b.options.Codec, msg.Body)
	if err != nil {
		return err
	}

	sendMsg := msg.Clone()
	sendMsg.Body = buf

	return b.publish(ctx, topic, sendMsg, opts...)
}

// publish 使用 XADD 命令将消息写入 Redis Stream
func (b *streamBroker) publish(_ context.Context, stream string, msg *broker.Message, opts ...broker.PublishOption) error {
	publishOpts := broker.PublishOptions{
		Context: context.Background(),
	}
	for _, o := range opts {
		o(&publishOpts)
	}

	// pool 为 nil 时 `(*Pool)(nil).Get()` 会 panic。三种情况会置 nil：
	// 未 Connect、Connect 探活失败、已 Disconnect。须前置防护。
	if b.getPool() == nil {
		return errors.New("redis-stream: not connected, call Connect first")
	}

	conn := b.getPool().Get()
	defer func() { _ = conn.Close() }()

	args := []any{stream}

	// MAXLEN 限制
	var maxLen int64
	if v, ok := publishOpts.Context.Value(redisOption.StreamMaxLenKey{}).(int64); ok {
		maxLen = v
	}
	if maxLen > 0 {
		args = append(args, "MAXLEN", "~", maxLen)
	}

	args = append(args, "*")
	args = append(args, "body", msg.BodyBytes())

	// 附加消息头（值转为 string）
	for k, v := range msg.Headers {
		args = append(args, k, fmt.Sprintf("%v", v))
	}

	_, err := conn.Do("XADD", args...)
	return err
}

func (b *streamBroker) Subscribe(topic string, handler broker.Handler, binder broker.Binder, opts ...broker.SubscribeOption) (broker.Subscriber, error) {
	// 标准构造器（AutoAck 默认 true 等）——裸字面量会让 AutoAck 落零值 false，
	// 消息投递后永不 XACK、PEL 永久滞留（段 5 实测钉住的缺陷）。
	subOpts := broker.NewSubscribeOptions()
	subOpts.Context = context.Background()
	for _, o := range opts {
		o(&subOpts)
	}

	if len(b.options.SubscriberMiddlewares) > 0 {
		handler = broker.ChainSubscriberMiddleware(handler, b.options.SubscriberMiddlewares)
	}

	// pool 为 nil 时 `(*Pool)(nil).Get()` 会 panic（见 publish 注释）。
	// 此处前置防护，同时覆盖后续的 ensureGroup 与 conn 获取。
	if b.getPool() == nil {
		return nil, errors.New("redis-stream: not connected, call Connect first")
	}

	// 提取 Stream 专属配置
	group := redisOption.DefaultStreamGroup
	if v, ok := subOpts.Context.Value(redisOption.StreamGroupKey{}).(string); ok && v != "" {
		group = v
	}

	consumer := redisOption.DefaultStreamConsumer
	if v, ok := subOpts.Context.Value(redisOption.StreamConsumerKey{}).(string); ok && v != "" {
		consumer = v
	}

	blockTime := redisOption.DefaultStreamBlockTime
	if v, ok := subOpts.Context.Value(redisOption.StreamBlockTimeKey{}).(time.Duration); ok && v > 0 {
		blockTime = v
	}

	count := redisOption.DefaultStreamCount
	if v, ok := subOpts.Context.Value(redisOption.StreamCountKey{}).(int); ok && v > 0 {
		count = v
	}

	// 确保消费组存在
	if err := b.ensureGroup(topic, group); err != nil {
		return nil, err
	}

	sub := &subscriber{
		b:         b,
		topic:     topic,
		group:     group,
		consumer:  consumer,
		blockTime: blockTime,
		count:     count,
		handler:   handler,
		binder:    binder,
		options:   subOpts,
	}

	b.subscribers.Add(topic, sub)

	go sub.recv()

	return sub, nil
}

// ensureGroup 确保消费组存在，不存在则创建
func (b *streamBroker) ensureGroup(stream, group string) error {
	pool := b.getPool()
	if pool == nil {
		return errors.New("redis-stream: broker disconnected, cannot ensure group")
	}
	conn := pool.Get()
	defer func() { _ = conn.Close() }()

	// XGROUP CREATE stream group $ MKSTREAM
	_, err := conn.Do("XGROUP", "CREATE", stream, group, "$", "MKSTREAM")
	if err != nil {
		// BUSYGROUP: Consumer Group name already exists 是正常的
		if strings.Contains(err.Error(), "BUSYGROUP") {
			return nil
		}
		return err
	}
	return nil
}
