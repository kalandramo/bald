package rabbitmq

import (
	"context"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/kalandramo/bald/broker"
)

///
/// Option
///

type exchangeDurableKey struct{}
type exchangeNameKey struct{}
type exchangeKindKey struct{}

type prefetchCountKey struct{}
type prefetchSizeKey struct{}
type prefetchGlobalKey struct{}
type externalAuthKey struct{}

// WithDurableExchange Exchange.Durable
func WithDurableExchange() broker.Option {
	return broker.OptionContextWithValue(exchangeDurableKey{}, true)
}

// WithExchangeName Exchange.Name
func WithExchangeName(name string) broker.Option {
	return broker.OptionContextWithValue(exchangeNameKey{}, name)
}

// WithExchangeType Exchange.Type
func WithExchangeType(kind string) broker.Option {
	return broker.OptionContextWithValue(exchangeKindKey{}, kind)
}

// WithPrefetchCount Channel.Qos.PrefetchCount
func WithPrefetchCount(cnt int) broker.Option {
	return broker.OptionContextWithValue(prefetchCountKey{}, cnt)
}

// WithPrefetchSize Channel.Qos.PrefetchSize
func WithPrefetchSize(size int) broker.Option {
	return broker.OptionContextWithValue(prefetchSizeKey{}, size)
}

// WithPrefetchGlobal Channel.Qos.Global
func WithPrefetchGlobal() broker.Option {
	return broker.OptionContextWithValue(prefetchGlobalKey{}, true)
}

func WithExternalAuth() broker.Option {
	return broker.OptionContextWithValue(externalAuthKey{}, ExternalAuthentication{})
}

type exchangesKey struct{}
type defaultExchangeNameKey struct{}

// WithExchanges registers multiple exchanges on the connection.
// The first exchange in the list becomes the default if WithExchangeName is not used.
func WithExchanges(exchanges ...Exchange) broker.Option {
	return broker.OptionContextWithValue(exchangesKey{}, exchanges)
}

// WithDefaultExchange sets the default exchange name for publish/subscribe
// when no exchange is explicitly specified. The exchange must already be registered
// (via WithExchanges or the legacy WithExchangeName).
func WithDefaultExchange(name string) broker.Option {
	return broker.OptionContextWithValue(defaultExchangeNameKey{}, name)
}

type confirmModeKey struct{}
type onReturnKey struct{}
type onConfirmKey struct{}

type defaultQueueKey struct{}

// WithDefaultQueue sets the default queue name used by Subscribe when the
// explicit topic is empty (consumer of the contract broker.rabbitmq.queue
// section, wired by the contract Provider).
func WithDefaultQueue(name string) broker.Option {
	return broker.OptionContextWithValue(defaultQueueKey{}, name)
}

// ReturnHandler handles messages returned by the broker when they cannot be routed.
type ReturnHandler func(amqp.Return)

// ConfirmHandler handles publisher confirmations (ack or nack).
type ConfirmHandler func(amqp.Confirmation)

// WithConfirmMode enables publisher confirms on the publish channel.
// When enabled, the broker will acknowledge each published message.
func WithConfirmMode() broker.Option {
	return broker.OptionContextWithValue(confirmModeKey{}, true)
}

// WithOnReturn registers a callback for messages returned by the broker.
// Messages are returned when the mandatory flag is set and no queue is bound to match the routing key.
func WithOnReturn(handler ReturnHandler) broker.Option {
	return broker.OptionContextWithValue(onReturnKey{}, handler)
}

// WithOnConfirm registers a callback for publisher confirmations.
// Requires WithConfirmMode to be enabled.
func WithOnConfirm(handler ConfirmHandler) broker.Option {
	return broker.OptionContextWithValue(onConfirmKey{}, handler)
}

///
/// SubscribeOption
///

type durableQueueKey struct{}
type subscribeBindArgsKey struct{}
type subscribeQueueArgsKey struct{}
type requeueOnErrorKey struct{}
type subscribeContextKey struct{}
type ackSuccessKey struct{}
type autoDeleteQueueKey struct{}

func WithDurableQueue() broker.SubscribeOption {
	return broker.SubscribeContextWithValue(durableQueueKey{}, true)
}

func WithAutoDeleteQueue() broker.SubscribeOption {
	return broker.SubscribeContextWithValue(autoDeleteQueueKey{}, true)
}

func WithBindArguments(args map[string]any) broker.SubscribeOption {
	return broker.SubscribeContextWithValue(subscribeBindArgsKey{}, args)
}

func WithQueueArguments(args map[string]any) broker.SubscribeOption {
	return broker.SubscribeContextWithValue(subscribeQueueArgsKey{}, args)
}

func WithRequeueOnError() broker.SubscribeOption {
	return broker.SubscribeContextWithValue(requeueOnErrorKey{}, true)
}

func WithSubscribeContext(ctx context.Context) broker.SubscribeOption {
	return broker.SubscribeContextWithValue(subscribeContextKey{}, ctx)
}

func WithAckOnSuccess() broker.SubscribeOption {
	return broker.SubscribeContextWithValue(ackSuccessKey{}, true)
}

type subscribeExchangeKey struct{}

// WithSubscribeExchange specifies which exchange to bind the queue to for this subscription.
// If not set, the default exchange is used.
func WithSubscribeExchange(name string) broker.SubscribeOption {
	return broker.SubscribeContextWithValue(subscribeExchangeKey{}, name)
}

///
/// PublishOption
///

type DeclarePublishQueueInfo struct {
	QueueArguments map[string]any
	BindArguments  map[string]any
	Durable        bool
	AutoDelete     bool
	Queue          string
}

type deliveryModeKey struct{}
type priorityKey struct{}
type contentTypeKey struct{}
type contentEncodingKey struct{}
type correlationIDKey struct{}
type replyToKey struct{}
type expirationKey struct{}
type messageIDKey struct{}
type timestampKey struct{}
type messageTypeKey struct{}
type userIDKey struct{}
type appIDKey struct{}
type publishHeadersKey struct{}
type publishDeclareQueueKey struct{}
type mandatoryKey struct{}

// WithDeliveryMode amqp.Publishing.DeliveryMode
func WithDeliveryMode(value uint8) broker.PublishOption {
	return broker.PublishContextWithValue(deliveryModeKey{}, value)
}

// WithPriority amqp.Publishing.Priority
func WithPriority(value uint8) broker.PublishOption {
	return broker.PublishContextWithValue(priorityKey{}, value)
}

// WithContentType amqp.Publishing.ContentType
func WithContentType(value string) broker.PublishOption {
	return broker.PublishContextWithValue(contentTypeKey{}, value)
}

// WithContentEncoding amqp.Publishing.ContentEncoding
func WithContentEncoding(value string) broker.PublishOption {
	return broker.PublishContextWithValue(contentEncodingKey{}, value)
}

// WithCorrelationID amqp.Publishing.CorrelationId
func WithCorrelationID(value string) broker.PublishOption {
	return broker.PublishContextWithValue(correlationIDKey{}, value)
}

// WithReplyTo amqp.Publishing.ReplyTo
func WithReplyTo(value string) broker.PublishOption {
	return broker.PublishContextWithValue(replyToKey{}, value)
}

// WithExpiration amqp.Publishing.Expiration
func WithExpiration(value string) broker.PublishOption {
	return broker.PublishContextWithValue(expirationKey{}, value)
}

// WithMessageId amqp.Publishing.MessageId
func WithMessageId(value string) broker.PublishOption {
	return broker.PublishContextWithValue(messageIDKey{}, value)
}

// WithTimestamp amqp.Publishing.Timestamp
func WithTimestamp(value time.Time) broker.PublishOption {
	return broker.PublishContextWithValue(timestampKey{}, value)
}

// WithTypeMsg amqp.Publishing.Type
func WithTypeMsg(value string) broker.PublishOption {
	return broker.PublishContextWithValue(messageTypeKey{}, value)
}

// WithUserID amqp.Publishing.UserId
func WithUserID(value string) broker.PublishOption {
	return broker.PublishContextWithValue(userIDKey{}, value)
}

// WithAppID amqp.Publishing.AppId
func WithAppID(value string) broker.PublishOption {
	return broker.PublishContextWithValue(appIDKey{}, value)
}

// WithPublishHeaders amqp.Publishing.Headers
func WithPublishHeaders(h map[string]any) broker.PublishOption {
	return broker.PublishContextWithValue(publishHeadersKey{}, h)
}

// WithPublishDeclareQueue publish declare queue info
func WithPublishDeclareQueue(queueName string, durableQueue, autoDelete bool, queueArgs map[string]any, bindArgs map[string]any) broker.PublishOption {
	val := &DeclarePublishQueueInfo{
		Queue:          queueName,
		Durable:        durableQueue,
		AutoDelete:     autoDelete,
		QueueArguments: queueArgs,
		BindArguments:  bindArgs,
	}
	return broker.PublishContextWithValue(publishDeclareQueueKey{}, val)
}

// WithMandatory sets the mandatory flag for publishing.
// When true, if the message cannot be routed to any queue, the broker will return it
// (triggering the OnReturn callback if registered).
func WithMandatory() broker.PublishOption {
	return broker.PublishContextWithValue(mandatoryKey{}, true)
}

type publishExchangeKey struct{}

// WithPublishExchange specifies which exchange to publish to.
// If not set, the default exchange is used.
func WithPublishExchange(name string) broker.PublishOption {
	return broker.PublishContextWithValue(publishExchangeKey{}, name)
}
