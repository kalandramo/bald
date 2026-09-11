package option

import (
	"time"

	"github.com/kalandramo/bald/log"

	"github.com/kalandramo/bald/broker"
)

///
/// Option
///

type Credentials struct {
	AccessKey, AccessSecret, SecurityToken string
}

func WithLoggerLevel(level log.Level) broker.Option {
	return broker.OptionContextWithValue(LoggerLevelKey{}, level)
}

func WithEnableTrace() broker.Option {
	return broker.OptionContextWithValue(EnableTraceKey{}, true)
}

func WithNameServer(addrs []string) broker.Option {
	return broker.OptionContextWithValue(NameServersKey{}, addrs)
}
func WithNameServerDomain(uri string) broker.Option {
	return broker.OptionContextWithValue(NameServerUrlKey{}, uri)
}

func WithAccessKey(key string) broker.Option {
	return broker.OptionContextWithValue(AccessKey{}, key)
}
func WithSecretKey(secret string) broker.Option {
	return broker.OptionContextWithValue(SecretKey{}, secret)
}
func WithSecurityToken(token string) broker.Option {
	return broker.OptionContextWithValue(SecurityTokenKey{}, token)
}
func WithCredentials(accessKey, accessSecret, securityToken string) broker.Option {
	return broker.OptionContextWithValue(CredentialsKey{},
		&Credentials{
			AccessKey:     accessKey,
			AccessSecret:  accessSecret,
			SecurityToken: securityToken,
		},
	)
}

func WithRetryCount(count int) broker.Option {
	return broker.OptionContextWithValue(RetryCountKey{}, count)
}

func WithNamespace(ns string) broker.Option {
	return broker.OptionContextWithValue(NamespaceKey{}, ns)
}

func WithInstanceName(name string) broker.Option {
	return broker.OptionContextWithValue(InstanceNameKey{}, name)
}

func WithGroupName(name string) broker.Option {
	return broker.OptionContextWithValue(GroupNameKey{}, name)
}

type DefaultTopicKey struct{}

// WithDefaultTopic 设置默认订阅主题（契约 broker.rocketmq.topic 段消费者，
// 业务 Subscribe(topic) 时若显式 topic 为空则回退该主题）。
func WithDefaultTopic(topic string) broker.Option {
	return broker.OptionContextWithValue(DefaultTopicKey{}, topic)
}

///
/// PublishOption
///

func WithCompress(compress bool) broker.PublishOption {
	return broker.PublishContextWithValue(CompressKey{}, compress)
}

func WithBatch(batch bool) broker.PublishOption {
	return broker.PublishContextWithValue(BatchKey{}, batch)
}

func WithProperties(properties map[string]string) broker.PublishOption {
	return broker.PublishContextWithValue(PropertiesKey{}, properties)
}

func WithDelayTimeLevel(level int) broker.PublishOption {
	return broker.PublishContextWithValue(DelayTimeLevelKey{}, level)
}

func WithTag(tags string) broker.PublishOption {
	return broker.PublishContextWithValue(TagsKey{}, tags)
}

func WithKeys(keys []string) broker.PublishOption {
	return broker.PublishContextWithValue(KeysKey{}, keys)
}

func WithShardingKey(key string) broker.PublishOption {
	return broker.PublishContextWithValue(ShardingKeyKey{}, key)
}

func WithDeliveryTimestamp(deliveryTimestamp time.Time) broker.PublishOption {
	return broker.PublishContextWithValue(DeliveryTimestampKey{}, deliveryTimestamp)
}

func WithMessageGroup(group string) broker.PublishOption {
	return broker.PublishContextWithValue(MessageGroupKey{}, group)
}

func WithSendAsync(enable bool) broker.PublishOption {
	return broker.PublishContextWithValue(SendAsyncKey{}, enable)
}

func WithSendWithTransaction(enable bool) broker.PublishOption {
	return broker.PublishContextWithValue(SendWithTransactionKey{}, enable)
}

///
/// SubscribeOption
///

func WithConsumerModel(model MessageModel) broker.SubscribeOption {
	return broker.SubscribeContextWithValue(ConsumerModelKey{}, model)
}
