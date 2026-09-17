package etcd

import (
	"context"
	"time"
)

// options 汇集 etcd Registry 的全部可调参数。
type options struct {
	ctx         context.Context
	namespace   string
	ttl         time.Duration
	maxRetry    int
	dialTimeout time.Duration

	endpoints []string
	username  string
	password  string
}

// Option 是 etcd Registry 的函数式选项。
type Option func(o *options)

func newOptions(opts ...Option) options {
	o := options{
		ctx:         context.Background(),
		namespace:   "/microservices",
		ttl:         time.Second * 15,
		maxRetry:    5,
		dialTimeout: time.Second * 5,
	}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithCtx 设置根 context（heartBeat/订阅随其取消）。
func WithCtx(ctx context.Context) Option {
	return func(o *options) { o.ctx = ctx }
}

// WithNamespace 设置 key 前缀（默认 "/microservices"）。
func WithNamespace(ns string) Option {
	return func(o *options) { o.namespace = ns }
}

// WithRegisterTTL 设置注册租约 TTL（默认 15s）。
func WithRegisterTTL(ttl time.Duration) Option {
	return func(o *options) { o.ttl = ttl }
}

// WithMaxRetry 设置断链后重试重注册的最大次数（默认 5）。
func WithMaxRetry(maxRetry int) Option {
	return func(o *options) { o.maxRetry = maxRetry }
}

// WithDialTimeout 设置自建 client 的拨号超时（默认 5s）。
func WithDialTimeout(d time.Duration) Option {
	return func(o *options) { o.dialTimeout = d }
}

// WithEndpoints 设置 etcd endpoints（自建 client 模式必填）。
func WithEndpoints(endpoints ...string) Option {
	return func(o *options) { o.endpoints = endpoints }
}

// WithUsername 设置 etcd 认证用户名。
func WithUsername(u string) Option {
	return func(o *options) { o.username = u }
}

// WithPassword 设置 etcd 认证密码。
func WithPassword(p string) Option {
	return func(o *options) { o.password = p }
}
