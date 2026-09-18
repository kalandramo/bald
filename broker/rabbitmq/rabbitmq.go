package rabbitmq

import (
	"context"
	"errors"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/kalandramo/bald/broker"
)

const (
	ProtocolVersion = "0.9.1"
	Protocol        = "AMQP"
)

type rabbitBroker struct {
	mtx sync.Mutex
	wg  sync.WaitGroup

	conn    *rabbitConnection
	options broker.Options

	subscribers *broker.SubscriberSyncMap
}

func NewBroker(opts ...broker.Option) broker.Broker {
	options := broker.NewOptionsAndApply(opts...)

	b := &rabbitBroker{
		options:     options,
		subscribers: broker.NewSubscriberSyncMap(),
	}

	return b
}

func (b *rabbitBroker) Name() string {
	return "rabbitmq"
}

func (b *rabbitBroker) Options() broker.Options {
	return b.options
}

func (b *rabbitBroker) Address() string {
	if len(b.options.Addrs) > 0 {
		return b.options.Addrs[0]
	}
	return ""
}

func (b *rabbitBroker) Init(opts ...broker.Option) error {
	b.options.Apply(opts...)

	var addrs []string
	for _, addr := range b.options.Addrs {
		if len(addr) == 0 {
			continue
		}
		if !hasUrlPrefix(addr) {
			addr = "amqp://" + addr
		}
		addrs = append(addrs, addr)
	}
	if len(addrs) == 0 {
		addrs = []string{DefaultRabbitURL}
	}
	b.options.Addrs = addrs

	return nil
}

func (b *rabbitBroker) Connect() error {
	if b.conn == nil {
		b.conn = newRabbitMQConnection(b.options)
	}

	conf := DefaultAmqpConfig

	if auth, ok := b.options.Context.Value(externalAuthKey{}).(ExternalAuthentication); ok {
		conf.SASL = []amqp.Authentication{&auth}
	}

	conf.TLSClientConfig = b.options.TLSConfig

	return b.conn.Connect(b.options.Secure, &conf)
}

func (b *rabbitBroker) Disconnect() error {
	if b.conn == nil {
		return errors.New("connection is nil")
	}

	b.subscribers.Clear()

	ret := b.conn.Close()
	b.wg.Wait()

	return ret
}

func (b *rabbitBroker) Publish(ctx context.Context, routingKey string, msg *broker.Message, opts ...broker.PublishOption) error {
	var finalTask = b.internalPublish

	if len(b.options.PublishMiddlewares) > 0 {
		finalTask = broker.ChainPublishMiddleware(finalTask, b.options.PublishMiddlewares)
	}

	return finalTask(ctx, routingKey, msg, opts...)
}

func (b *rabbitBroker) internalPublish(ctx context.Context, topic string, msg *broker.Message, opts ...broker.PublishOption) error {
	buf, err := broker.Marshal(b.options.Codec, msg.Body)
	if err != nil {
		return err
	}

	sendMsg := msg.Clone()
	sendMsg.Body = buf

	return b.publish(ctx, topic, sendMsg, opts...)
}

func (b *rabbitBroker) publish(ctx context.Context, routingKey string, msg *broker.Message, opts ...broker.PublishOption) error {
	if b.conn == nil {
		return errors.New("connection is nil")
	}

	options := broker.PublishOptions{
		Context: ctx,
	}
	for _, o := range opts {
		o(&options)
	}

	rMsg := amqp.Publishing{
		Body:    msg.BodyBytes(),
		Headers: amqp.Table{},
	}

	for k, v := range msg.Headers {
		rMsg.Headers[k] = v
	}

	if value, ok := options.Context.Value(deliveryModeKey{}).(uint8); ok {
		rMsg.DeliveryMode = value
	}

	if value, ok := options.Context.Value(priorityKey{}).(uint8); ok {
		rMsg.Priority = value
	}

	if value, ok := options.Context.Value(contentTypeKey{}).(string); ok {
		rMsg.ContentType = value
	}

	if value, ok := options.Context.Value(contentEncodingKey{}).(string); ok {
		rMsg.ContentEncoding = value
	}

	if value, ok := options.Context.Value(correlationIDKey{}).(string); ok {
		rMsg.CorrelationId = value
	}

	if value, ok := options.Context.Value(replyToKey{}).(string); ok {
		rMsg.ReplyTo = value
	}

	if value, ok := options.Context.Value(expirationKey{}).(string); ok {
		rMsg.Expiration = value
	}

	if value, ok := options.Context.Value(messageIDKey{}).(string); ok {
		rMsg.MessageId = value
	}

	if value, ok := options.Context.Value(timestampKey{}).(time.Time); ok {
		rMsg.Timestamp = value
	}

	if value, ok := options.Context.Value(messageTypeKey{}).(string); ok {
		rMsg.Type = value
	}

	if value, ok := options.Context.Value(userIDKey{}).(string); ok {
		rMsg.UserId = value
	}

	if value, ok := options.Context.Value(appIDKey{}).(string); ok {
		rMsg.AppId = value
	}

	if headers, ok := options.Context.Value(publishHeadersKey{}).(map[string]any); ok {
		for k, v := range headers {
			rMsg.Headers[k] = v
		}
	}

	// determine target exchange
	var exchangeName string
	if val, ok := options.Context.Value(publishExchangeKey{}).(string); ok && val != "" {
		exchangeName = val
	} else {
		exchangeName = b.conn.defaultExchangeName
	}

	if val, ok := options.Context.Value(publishDeclareQueueKey{}).(*DeclarePublishQueueInfo); ok {
		if val.Durable {
			val.AutoDelete = false
		}
		if err := b.conn.DeclarePublishQueue(exchangeName, val.Queue, routingKey, val.BindArguments, val.QueueArguments, val.Durable, val.AutoDelete); err != nil {
			return err
		}
	}

	var mandatory bool
	if val, ok := options.Context.Value(mandatoryKey{}).(bool); ok {
		mandatory = val
	}

	return b.conn.Publish(ctx, exchangeName, routingKey, mandatory, rMsg)
}

func (b *rabbitBroker) Subscribe(routingKey string, handler broker.Handler, binder broker.Binder, opts ...broker.SubscribeOption) (broker.Subscriber, error) {
	if b.conn == nil {
		return nil, errors.New("not connected")
	}

	if routingKey == "" {
		if val, ok := b.options.Context.Value(defaultQueueKey{}).(string); ok {
			routingKey = val
		}
	}
	if routingKey == "" {
		return nil, errors.New("routing key (queue) is empty")
	}

	options := broker.SubscribeOptions{
		Context: context.Background(),
		AutoAck: true,
	}
	for _, o := range opts {
		o(&options)
	}

	if len(b.options.SubscriberMiddlewares) > 0 {
		handler = broker.ChainSubscriberMiddleware(handler, b.options.SubscriberMiddlewares)
	}

	var requeueOnError = false
	if val, ok := options.Context.Value(requeueOnErrorKey{}).(bool); ok {
		requeueOnError = val
	}

	var ackSuccess = false
	if val, ok := options.Context.Value(ackSuccessKey{}).(bool); ok && val {
		options.AutoAck = false
		ackSuccess = true
	}

	fn := func(msg amqp.Delivery) {
		m := &broker.Message{
			Headers: rabbitHeaderToMap(msg.Headers),
			Body:    nil,
		}

		ctx := options.Context

		p := &publication{d: msg, message: m, topic: msg.RoutingKey}

		if binder != nil {
			m.Body = binder()

			if p.err = broker.Unmarshal(b.options.Codec, msg.Body, &m.Body); p.err != nil {
				LogErrorf("unmarshal message failed: %v", p.err)
			}
		} else {
			m.Body = msg.Body
		}

		p.err = handler(ctx, p)
		if p.err == nil && ackSuccess && !options.AutoAck {
			_ = msg.Ack(false)
		} else if p.err != nil && !options.AutoAck {
			_ = msg.Nack(false, requeueOnError)
		}
	}

	// determine target exchange for subscription
	var subscribeExchangeName string
	if val, ok := options.Context.Value(subscribeExchangeKey{}).(string); ok && val != "" {
		subscribeExchangeName = val
	} else {
		subscribeExchangeName = b.conn.defaultExchangeName
	}

	sub := &subscriber{
		topic:        routingKey,
		options:      options,
		r:            b,
		exchangeName: subscribeExchangeName,
		durableQueue: true,
		autoDelete:   false,
		fn:           fn,
		headers:      nil,
		queueArgs:    nil,
	}

	if val, ok := options.Context.Value(durableQueueKey{}).(bool); ok {
		sub.durableQueue = val
		sub.autoDelete = false
	}

	if val, ok := options.Context.Value(autoDeleteQueueKey{}).(bool); ok {
		sub.autoDelete = val
		sub.durableQueue = false
	}

	if val, ok := options.Context.Value(subscribeBindArgsKey{}).(map[string]any); ok {
		sub.headers = val
	}

	if val, ok := options.Context.Value(subscribeQueueArgsKey{}).(map[string]any); ok {
		sub.queueArgs = val
	}

	b.subscribers.Add(routingKey, sub)

	go sub.resubscribe()

	return sub, nil
}
