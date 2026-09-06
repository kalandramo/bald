// Package contract 提供 rabbitmq 后端的契约装配：bootstrapv1.Broker 的 rabbitmq
// 段 → broker.Option 映射 + BrokerRegistry Provider。
//
// 单独成包的原因：rabbitmq 实现保持零契约依赖（纯 SDK 封装），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/rabbitmq"
)

// Type 是契约 broker 段中 rabbitmq 后端的段名。
const Type = "rabbitmq"

// Provider 按契约 broker.rabbitmq 段构造 RabbitMQ Broker 并建立连接。
// 返回的 cleanup 断开连接并清理订阅者。仅当 broker.rabbitmq 段存在时被
// BrokerRegistry 调度到。签名与 appkit.BrokerProvider 结构化兼容，直接注册：
//
//	br := appkit.NewBrokerRegistry()
//	br.MustRegister(rabbitmqcontract.Type, rabbitmqcontract.Provider)
//
// 字段消费：url=连接地址（缺 amqp:// 前缀自动补）、username/password=凭证
// （URL 无 userinfo 时注入）、exchange=默认 exchange、queue=默认订阅队列。
func Provider(ctx context.Context, cfg *bootstrapv1.Broker) (any, func(), error) {
	sec := cfg.GetRabbitmq()
	if sec == nil {
		return nil, nil, fmt.Errorf("broker: type=%q but rabbitmq section is missing", Type)
	}

	rawURL := sec.GetUrl()
	if rawURL == "" {
		return nil, nil, fmt.Errorf("broker.rabbitmq: url is required")
	}

	addr, err := injectCredentials(rawURL, sec.GetUsername(), sec.GetPassword())
	if err != nil {
		return nil, nil, fmt.Errorf("broker.rabbitmq: invalid url: %w", err)
	}

	var opts []broker.Option
	opts = append(opts, broker.WithAddress(addr))

	if exchange := sec.GetExchange(); exchange != "" {
		opts = append(opts, rabbitmq.WithDefaultExchange(exchange))
	}
	if queue := sec.GetQueue(); queue != "" {
		opts = append(opts, rabbitmq.WithDefaultQueue(queue))
	}

	b := rabbitmq.NewBroker(opts...)
	if err := b.Init(); err != nil {
		return nil, nil, fmt.Errorf("broker.rabbitmq: init: %w", err)
	}
	if err := b.Connect(); err != nil {
		return nil, nil, fmt.Errorf("broker.rabbitmq: connect: %w", err)
	}
	return b, func() { _ = b.Disconnect() }, nil
}

// injectCredentials 在 URL 无 userinfo 时注入契约凭证。
func injectCredentials(rawURL, username, password string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("empty url")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.User != nil && u.User.String() != "" {
		return rawURL, nil
	}
	if username == "" && password == "" {
		return rawURL, nil
	}

	u.User = url.UserPassword(username, password)
	return u.String(), nil
}

// 类型守卫：Provider 签名与 appkit.BrokerProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Broker) (any, func(), error) = Provider
