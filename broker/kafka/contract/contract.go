// Package contract 提供 kafka 后端的契约装配：bootstrapv1.Broker 的 kafka
// 段 → broker.Option 映射 + BrokerRegistry Provider。
//
// 单独成包的原因：kafka 实现保持零契约依赖（纯 SDK 封装），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"
	"strings"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/kafka"
)

// Type 是契约 broker 段中 kafka 后端的段名。
const Type = "kafka"

// Provider 按契约 broker.kafka 段构造 Kafka Broker 并建立连接。
// 返回的 cleanup 断开连接并清理订阅者。仅当 broker.kafka 段存在时被
// BrokerRegistry 调度到。签名与 appkit.BrokerProvider 结构化兼容，直接注册：
//
//	br := appkit.NewBrokerRegistry()
//	br.MustRegister(kafkacontract.Type, kafkacontract.Provider)
//
// auth_type 取值："none"（默认）、"plain"、"scram-sha256"、"scram-sha512"。
func Provider(ctx context.Context, cfg *bootstrapv1.Broker) (any, func(), error) {
	sec := cfg.GetKafka()
	if sec == nil {
		return nil, nil, fmt.Errorf("broker: type=%q but kafka section is missing", Type)
	}

	var opts []broker.Option
	if brokers := sec.GetBrokers(); len(brokers) > 0 {
		opts = append(opts, broker.WithAddress(brokers...))
	}

	switch strings.ToLower(sec.GetAuthType()) {
	case "", "none":
		// 匿名连接
	case "plain":
		opts = append(opts, kafka.WithPlainMechanism(sec.GetUsername(), sec.GetPassword()))
	case "scram-sha256":
		opts = append(opts, kafka.WithScramMechanism(kafka.ScramAlgorithmSHA256, sec.GetUsername(), sec.GetPassword()))
	case "scram-sha512":
		opts = append(opts, kafka.WithScramMechanism(kafka.ScramAlgorithmSHA512, sec.GetUsername(), sec.GetPassword()))
	default:
		return nil, nil, fmt.Errorf("broker.kafka: unsupported auth_type %q", sec.GetAuthType())
	}

	b := kafka.NewBroker(opts...)
	if err := b.Init(); err != nil {
		return nil, nil, fmt.Errorf("broker.kafka: init: %w", err)
	}
	if err := b.Connect(); err != nil {
		return nil, nil, fmt.Errorf("broker.kafka: connect: %w", err)
	}
	return b, func() { _ = b.Disconnect() }, nil
}

// 类型守卫：Provider 签名与 appkit.BrokerProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Broker) (any, func(), error) = Provider
