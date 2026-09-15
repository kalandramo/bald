// Package contract 提供 rocketmq 后端的契约装配：bootstrapv1.Broker 的 rocketmq
// 段 → broker.Option 映射 + BrokerRegistry Provider。
//
// 单独成包的原因：rocketmq 实现保持零契约依赖（纯 SDK 封装），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
//
// 后端为 Apache RocketMQ v2 客户端（rocketmq-client-go）：契约字段
// name_servers/name_server_url/access_key/secret_key/security_token/
// group_name/namespace/instance_name/topic/retry_count 全部落到 v2 客户端。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/rocketmq/option"
	v2 "github.com/kalandramo/bald/broker/rocketmq/v2"
)

// Type 是契约 broker 段中 rocketmq 后端的段名。
const Type = "rocketmq"

// Provider 按契约 broker.rocketmq 段构造 RocketMQ Broker 并连接 NameServer。
// 返回的 cleanup 断开生产者与订阅者。仅当 broker.rocketmq 段存在时被
// BrokerRegistry 调度到。签名与 bootstrap.BrokerProvider 结构化兼容，直接注册：
//
//	br := bootstrap.NewBrokerRegistry()
//	br.MustRegister(rocketmqcontract.Type, rocketmqcontract.Provider)
func Provider(ctx context.Context, cfg *bootstrapv1.Broker) (any, func(), error) {
	sec := cfg.GetRocketmq()
	if sec == nil {
		return nil, nil, fmt.Errorf("broker: type=%q but rocketmq section is missing", Type)
	}

	nameServers := sec.GetNameServers()
	if len(nameServers) == 0 && sec.GetNameServerUrl() == "" {
		return nil, nil, fmt.Errorf("broker.rocketmq: name_servers or name_server_url is required")
	}

	var opts []broker.Option
	if len(nameServers) > 0 {
		opts = append(opts, option.WithNameServer(nameServers))
	}
	if uri := sec.GetNameServerUrl(); uri != "" {
		opts = append(opts, option.WithNameServerDomain(uri))
	}
	if v := sec.GetAccessKey(); v != "" {
		opts = append(opts, option.WithCredentials(v, sec.GetSecretKey(), sec.GetSecurityToken()))
	}
	if v := sec.GetGroupName(); v != "" {
		opts = append(opts, option.WithGroupName(v))
	}
	if v := sec.GetNamespace(); v != "" {
		opts = append(opts, option.WithNamespace(v))
	}
	if v := sec.GetInstanceName(); v != "" {
		opts = append(opts, option.WithInstanceName(v))
	}
	if v := sec.GetRetryCount(); v != 0 {
		opts = append(opts, option.WithRetryCount(int(v)))
	}
	if v := sec.GetTopic(); v != "" {
		opts = append(opts, option.WithDefaultTopic(v))
	}

	b := v2.NewBroker(opts...)
	if err := b.Init(); err != nil {
		return nil, nil, fmt.Errorf("broker.rocketmq: init: %w", err)
	}
	if err := b.Connect(); err != nil {
		return nil, nil, fmt.Errorf("broker.rocketmq: connect: %w", err)
	}
	return b, func() { _ = b.Disconnect() }, nil
}

// 类型守卫：Provider 签名与 bootstrap.BrokerProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Broker) (any, func(), error) = Provider
