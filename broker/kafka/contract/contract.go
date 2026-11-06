// Package contract 提供 kafka 后端的契约装配：bootstrapv1.Broker 的 kafka
// 段 → broker.Option 映射 + 构造 func（供段驱动 Provider 的 Build 复用）。
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
// 返回的 cleanup 断开连接并清理订阅者。供自定义 Provider 的 Build 直接调用。
//
// auth_type 取值："none"（默认）、"plain"、"scram-sha256"、"scram-sha512"。
//
// 契约 broker.kafka.group_id **没有对应的 broker.Option**：本函数构造的是发布侧
// Broker（Init + Connect 只为出向发布建连），消费组属于订阅侧，而订阅侧的消费组
// 来自代码级参数（transport/kafka 服务器注册订阅者时给的 queue），契约里没有它
// 的落点。写成配置不会生效——与 bootstrap/env.go 里 Env.separator 同口径（登记册 §7.33）。
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

// 类型守卫：Provider 保持构造 func 形状（供 Provider.Build 复用）。
var _ func(context.Context, *bootstrapv1.Broker) (any, func(), error) = Provider
