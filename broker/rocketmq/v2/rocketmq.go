package rocketmq

import (
	"context"
	"errors"
	"sync"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
	"github.com/apache/rocketmq-client-go/v2/rlog"

	"github.com/kalandramo/bald/log"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/rocketmq/option"
)

type rocketmqBroker struct {
	sync.RWMutex

	nameServers   []string
	nameServerUrl string

	credentials option.Credentials

	retryCount int

	instanceName string
	groupName    string
	namespace    string

	enableTrace bool

	connected bool
	options   broker.Options

	producers   map[string]rocketmq.Producer
	subscribers *broker.SubscriberSyncMap
}

// sdkLogger 是本包注进 SDK 日志全局（rlog 的 `rLog`）的那一个转发器，进程内共用。
//
// 为什么共用、且只注入一次：rlog 的读侧不带锁（rlog/log.go:201-225 直接调那个全局接口值），
// 而接口值是两个字（类型 + 数据）。每次 Init 重写它，在 -race 下是一串 DATA RACE，
// 在真机上等价于「后台日志线程可能读到半个指针」。SDK 也不会告诉 bald 某行日志出自
// 哪台 broker，所以「每台 broker 一个 logger」本来就没有可观察的意义。
//
// 级别仍是每个 broker 的 Init 能设的（LoggerLevelKey ⇒ setLevel），覆盖语义与改动前一致
// （后一次 Init 生效），只是读写都走原子。
var (
	sdkLogger     = newLogger(log.LevelInfo)
	publishLogger sync.Once
)

func NewBroker(opts ...broker.Option) broker.Broker {
	options := broker.NewOptionsAndApply(opts...)

	return &rocketmqBroker{
		options:     options,
		retryCount:  2,
		producers:   make(map[string]rocketmq.Producer),
		subscribers: broker.NewSubscriberSyncMap(),
	}
}

func (b *rocketmqBroker) Name() string {
	return "rocketmqV2"
}

func (b *rocketmqBroker) Address() string {
	if len(b.nameServers) > 0 {
		return b.nameServers[0]
	} else if b.nameServerUrl != "" {
		return b.nameServerUrl
	}
	return option.DefaultAddr
}

func (b *rocketmqBroker) Options() broker.Options {
	return b.options
}

func (b *rocketmqBroker) Init(opts ...broker.Option) error {
	b.options.Apply(opts...)

	// 只在本包第一次 Init 时注进 SDK 全局——那一刻本包还没造过任何客户端，
	// 也就没有并发的读侧（sdkLogger 的注释）。
	publishLogger.Do(func() { rlog.SetLogger(sdkLogger) })

	if v, ok := b.options.Context.Value(option.NameServersKey{}).([]string); ok {
		b.nameServers = v
	}
	if v, ok := b.options.Context.Value(option.NameServerUrlKey{}).(string); ok {
		b.nameServerUrl = v
	}
	if v, ok := b.options.Context.Value(option.AccessKey{}).(string); ok {
		b.credentials.AccessKey = v
	}
	if v, ok := b.options.Context.Value(option.SecretKey{}).(string); ok {
		b.credentials.AccessSecret = v
	}
	if v, ok := b.options.Context.Value(option.SecurityTokenKey{}).(string); ok {
		b.credentials.SecurityToken = v
	}
	if v, ok := b.options.Context.Value(option.CredentialsKey{}).(*option.Credentials); ok {
		b.credentials = *v
	}
	if v, ok := b.options.Context.Value(option.RetryCountKey{}).(int); ok {
		b.retryCount = v
	}
	if v, ok := b.options.Context.Value(option.NamespaceKey{}).(string); ok {
		b.namespace = v
	}
	if v, ok := b.options.Context.Value(option.InstanceNameKey{}).(string); ok {
		b.instanceName = v
	}
	if v, ok := b.options.Context.Value(option.GroupNameKey{}).(string); ok {
		b.groupName = v
	}
	if v, ok := b.options.Context.Value(option.EnableTraceKey{}).(bool); ok {
		b.enableTrace = v
	}

	if v, ok := b.options.Context.Value(option.LoggerLevelKey{}).(log.Level); ok {
		sdkLogger.setLevel(v)
	}

	return nil
}

func (b *rocketmqBroker) Connect() error {
	b.RLock()
	if b.connected {
		b.RUnlock()
		return nil
	}
	b.RUnlock()

	b.Lock()
	b.connected = true
	b.Unlock()

	return nil
}

func (b *rocketmqBroker) Disconnect() error {
	b.RLock()
	if !b.connected {
		b.RUnlock()
		return nil
	}
	b.RUnlock()

	b.Lock()
	defer b.Unlock()
	for _, p := range b.producers {
		if err := p.Shutdown(); err != nil {
			return err
		}
	}
	b.producers = make(map[string]rocketmq.Producer)

	// 注销全部订阅者并清空注册表（对齐 kafka 的 subscribers.Clear() 范式）。Clear() 对
	// 每一条调 Unsubscribe(false)，而 Unsubscribe 会 Shutdown 那个 consumer——消费/拉取/
	// 心跳线程与到 broker 的连接随之释放，进程里不再留下调用方以为已经摘掉的后台流量。
	if b.subscribers != nil {
		b.subscribers.Clear()
	}

	b.connected = false
	return nil
}

func (b *rocketmqBroker) createNsResolver() primitive.NsResolver {
	if len(b.nameServers) > 0 {
		return primitive.NewPassthroughResolver(b.nameServers)
	} else if b.nameServerUrl != "" {
		return primitive.NewHttpResolver("DEFAULT", b.nameServerUrl)
	} else {
		return primitive.NewHttpResolver("DEFAULT", option.DefaultAddr)
	}
}

func (b *rocketmqBroker) makeCredentials() *primitive.Credentials {
	return &primitive.Credentials{
		AccessKey:     b.credentials.AccessKey,
		SecretKey:     b.credentials.AccessSecret,
		SecurityToken: b.credentials.SecurityToken,
	}
}

func (b *rocketmqBroker) createProducer() (rocketmq.Producer, error) {
	credentials := b.makeCredentials()

	resolver := b.createNsResolver()

	var opts []producer.Option

	var traceCfg *primitive.TraceConfig = nil
	if b.enableTrace {
		traceCfg = &primitive.TraceConfig{
			Access:   primitive.Cloud,
			Resolver: resolver,
		}

		if b.groupName == "" {
			traceCfg.GroupName = "DEFAULT"
		} else {
			traceCfg.GroupName = b.groupName
		}

		if credentials.AccessKey != "" && credentials.SecretKey != "" {
			traceCfg.Credentials = *credentials
		}

		opts = append(opts, producer.WithTrace(traceCfg))
	}

	if credentials.AccessKey != "" && credentials.SecretKey != "" {
		opts = append(opts, producer.WithCredentials(*credentials))
	}

	opts = append(opts, producer.WithNsResolver(resolver))
	opts = append(opts, producer.WithRetry(b.retryCount))
	// namespace 对生产侧同样要落：SDK 在 SendSync 里把 msg.Topic 改成 `ns%topic`，
	// 少投这一行时生产者发的是裸 topic，与消费侧（createConsumer 一直投这行）落在两个
	// 主题上——命名空间租户下表现为「发得出去、收不到」。空 namespace 时 SDK 首行判空短路，
	// 所以对没用这一字段的老用户零影响。
	opts = append(opts, producer.WithNamespace(b.namespace))
	if b.instanceName != "" {
		opts = append(opts, producer.WithInstanceName(b.instanceName))
	}
	if b.groupName != "" {
		opts = append(opts, producer.WithGroupName(b.groupName))
	}

	p, err := rocketmq.NewProducer(opts...)
	if err != nil {
		sdkLogger.Errorf("new producer error: %s", err.Error())
		return nil, err
	}

	err = p.Start()
	if err != nil {
		sdkLogger.Errorf("[rocketmq]: start producer error: %s", err.Error())
		return nil, err
	}

	return p, nil
}

func (b *rocketmqBroker) createConsumer(options *broker.SubscribeOptions) (rocketmq.PushConsumer, error) {

	consumerOptions := []consumer.Option{
		consumer.WithGroupName(options.Queue),
		consumer.WithAutoCommit(options.AutoAck),
		consumer.WithRetry(b.retryCount),
		consumer.WithNamespace(b.namespace),
		consumer.WithInstance(b.instanceName),
	}

	credentials := b.makeCredentials()
	consumerOptions = append(consumerOptions, consumer.WithCredentials(*credentials))

	resolver := b.createNsResolver()
	consumerOptions = append(consumerOptions, consumer.WithNsResolver(resolver))

	if v, ok := options.Context.Value(option.ConsumerModelKey{}).(option.MessageModel); ok {
		var m consumer.MessageModel
		switch v {
		case option.MessageModelClustering:
			m = consumer.Clustering
		case option.MessageModelBroadCasting:
			m = consumer.BroadCasting
		}
		consumerOptions = append(consumerOptions, consumer.WithConsumerModel(m))
	}

	// 消息追踪
	// 注意：阿里云线上的RocketMQ一定要加此选项，不然会导致消费不成功
	if b.enableTrace {
		traceCfg := &primitive.TraceConfig{
			GroupName:   options.Queue,
			Credentials: *credentials,
			Access:      primitive.Cloud,
			Resolver:    resolver,
		}
		consumerOptions = append(consumerOptions, consumer.WithTrace(traceCfg))
	}

	c, err := rocketmq.NewPushConsumer(consumerOptions...)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (b *rocketmqBroker) Publish(ctx context.Context, topic string, msg *broker.Message, opts ...broker.PublishOption) error {
	var finalTask = b.internalPublish

	if len(b.options.PublishMiddlewares) > 0 {
		finalTask = broker.ChainPublishMiddleware(finalTask, b.options.PublishMiddlewares)
	}

	return finalTask(ctx, topic, msg, opts...)
}

func (b *rocketmqBroker) internalPublish(ctx context.Context, topic string, msg *broker.Message, opts ...broker.PublishOption) error {
	buf, err := broker.Marshal(b.options.Codec, msg.Body)
	if err != nil {
		return err
	}

	sendMsg := msg.Clone()
	sendMsg.Body = buf

	return b.publish(ctx, topic, sendMsg, opts...)
}

func (b *rocketmqBroker) publish(ctx context.Context, topic string, msg *broker.Message, opts ...broker.PublishOption) error {
	options := broker.PublishOptions{
		Context: ctx,
	}
	for _, o := range opts {
		o(&options)
	}

	var cached bool

	b.Lock()
	p, ok := b.producers[topic]
	if !ok {
		var err error
		p, err = b.createProducer()
		if err != nil {
			b.Unlock()
			return err
		}

		b.producers[topic] = p
	} else {
		cached = true
	}
	b.Unlock()

	rMsg := primitive.NewMessage(topic, msg.BodyBytes())

	if len(msg.Headers) > 0 {
		rMsg.WithProperties(msg.Headers)
	}

	if v, ok := options.Context.Value(option.CompressKey{}).(bool); ok {
		rMsg.Compress = v
	}
	if v, ok := options.Context.Value(option.BatchKey{}).(bool); ok {
		rMsg.Batch = v
	}
	if v, ok := options.Context.Value(option.PropertiesKey{}).(map[string]string); ok {
		rMsg.WithProperties(v)
	}
	if v, ok := options.Context.Value(option.DelayTimeLevelKey{}).(int); ok {
		rMsg.WithDelayTimeLevel(v)
	}
	if v, ok := options.Context.Value(option.TagsKey{}).(string); ok {
		rMsg.WithTag(v)
	}
	if v, ok := options.Context.Value(option.KeysKey{}).([]string); ok {
		rMsg.WithKeys(v)
	}
	if v, ok := options.Context.Value(option.ShardingKeyKey{}).(string); ok {
		rMsg.WithShardingKey(v)
	}

	var err error
	_, err = p.SendSync(ctx, rMsg)
	if err != nil {
		sdkLogger.Errorf("[rocketmq]: send message error: %s\n", err)

		// If using a cached producer, remove it and retry with a new one
		if cached {
			b.Lock()
			_ = p.Shutdown()
			delete(b.producers, topic)
			b.Unlock()

			var retryErr error
			p, retryErr = b.createProducer()
			if retryErr != nil {
				return err
			}

			if _, retryErr = p.SendSync(ctx, rMsg); retryErr != nil {
				return retryErr
			}

			err = nil // retry succeeded, clear original error

			b.Lock()
			b.producers[topic] = p
			b.Unlock()
		}
	}

	return err
}

func (b *rocketmqBroker) Subscribe(topic string, handler broker.Handler, binder broker.Binder, opts ...broker.SubscribeOption) (broker.Subscriber, error) {
	if topic == "" {
		if v, ok := b.options.Context.Value(option.DefaultTopicKey{}).(string); ok {
			topic = v
		}
	}
	if topic == "" {
		return nil, errors.New("rocketmq: topic is empty")
	}

	options := broker.SubscribeOptions{
		Context: context.Background(),
		AutoAck: true,
		Queue:   b.groupName,
	}
	for _, o := range opts {
		o(&options)
	}

	if len(b.options.SubscriberMiddlewares) > 0 {
		handler = broker.ChainSubscriberMiddleware(handler, b.options.SubscriberMiddlewares)
	}

	c, err := b.createConsumer(&options)
	if err != nil {
		return nil, err
	}

	sub := &subscriber{
		r:       b,
		options: options,
		topic:   topic,
		handler: handler,
		reader:  c,
	}

	if err = c.Subscribe(topic, consumer.MessageSelector{},
		func(ctx context.Context, msgs ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
			var errSub error
			hasError := false
			for _, msg := range msgs {
				var m broker.Message
				p := &publication{topic: msg.Topic, reader: sub.reader, m: &m, rm: &msg.Message, ctx: options.Context}

				m.Headers = msg.GetProperties()

				if binder != nil {
					m.Body = binder()

					if errSub = broker.Unmarshal(b.options.Codec, msg.Body, &m.Body); errSub != nil {
						p.err = errSub
						sdkLogger.Errorf("%s", errSub.Error())
						hasError = true
						continue
					}
				} else {
					m.Body = msg.Body
				}

				if errSub = sub.handler(ctx, p); errSub != nil {
					sdkLogger.Errorf("process message failed: %v", errSub)
					hasError = true
					continue
				}

				if sub.options.AutoAck {
					if errSub = p.Ack(); errSub != nil {
						sdkLogger.Errorf("unable to commit msg: %v", errSub)
						hasError = true
						continue
					}
				}
			}

			if hasError {
				return consumer.ConsumeRetryLater, nil
			}
			return consumer.ConsumeSuccess, nil
		}); err != nil {
		sdkLogger.Errorf("%s", err.Error())
		return nil, err
	}

	if err = c.Start(); err != nil {
		sdkLogger.Errorf("%s", err.Error())
		return nil, err
	}

	// 注册表收下这个订阅者，Disconnect 的 Clear() 才注销得掉 consumer；
	// 条目也是 Unsubscribe(true) 摘除的对象（与 kafka/rabbitmq 的 Subscribe 尾部同形）。
	b.subscribers.Add(topic, sub)

	return sub, nil
}
