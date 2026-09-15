// Package contract 提供 redis 后端的契约装配：bootstrapv1.Broker 的 redis
// 段 → broker.Option 映射 + BrokerRegistry Provider。
//
// 单独成包的原因：redis 实现保持零契约依赖（纯 SDK 封装），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/redis"
	"github.com/kalandramo/bald/broker/redis/option"
)

// Type 是契约 broker 段中 redis 后端的段名。
const Type = "redis"

// Provider 按契约 broker.redis 段构造 Redis Broker（默认 pubsub 驱动）并连接。
// 返回的 cleanup 断开连接。仅当 broker.redis 段存在时被 BrokerRegistry 调度到。
// 签名与 bootstrap.BrokerProvider 结构化兼容，直接注册：
//
//	br := bootstrap.NewBrokerRegistry()
//	br.MustRegister(rediscontract.Type, rediscontract.Provider)
//
// 契约段为 Pub/Sub 形状（address/password/db）；Stream 驱动为能力层，
// 业务可用 redis.NewBroker(option.DriverTypeStream, ...) 显式选择。
func Provider(ctx context.Context, cfg *bootstrapv1.Broker) (any, func(), error) {
	sec := cfg.GetRedis()
	if sec == nil {
		return nil, nil, fmt.Errorf("broker: type=%q but redis section is missing", Type)
	}

	addr := sec.GetAddress()
	if addr == "" {
		return nil, nil, fmt.Errorf("broker.redis: address is required")
	}
	addr = buildDialURL(addr, sec.GetPassword(), int(sec.GetDb()))

	opts := []broker.Option{broker.WithAddress(addr)}

	b := redis.NewBroker(option.DriverTypePubSub, opts...)
	if err := b.Init(); err != nil {
		return nil, nil, fmt.Errorf("broker.redis: init: %w", err)
	}
	if err := b.Connect(); err != nil {
		return nil, nil, fmt.Errorf("broker.redis: connect: %w", err)
	}
	return b, func() { _ = b.Disconnect() }, nil
}

// buildDialURL 组装 redigo DialURL 可识别的连接地址：
// 裸地址补 redis:// 前缀，password/db 分别注入 userinfo 与 path。
func buildDialURL(addr, password string, db int) string {
	if !strings.Contains(addr, "://") {
		addr = "redis://" + addr
	}

	u, err := url.Parse(addr)
	if err != nil {
		return addr
	}
	if password != "" && u.User == nil {
		u.User = url.UserPassword("", password)
	}
	if db != 0 && u.Path == "" {
		u.Path = fmt.Sprintf("/%d", db)
	}
	return u.String()
}

// 类型守卫：Provider 签名与 bootstrap.BrokerProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Broker) (any, func(), error) = Provider
