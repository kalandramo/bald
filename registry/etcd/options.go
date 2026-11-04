package etcd

import (
	"context"
	"time"

	"github.com/kalandramo/bald/retry"
)

// options 汇集 etcd Registry 的全部可调参数。
type options struct {
	ctx         context.Context
	namespace   string
	ttl         time.Duration
	maxRetry    int
	dialTimeout time.Duration

	// retreat 是断链重注册的退避形状；零值即「两半都用默认」，见 retreatPolicy。
	retreat retreatPolicy

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
		retreat:     defaultRetreatPolicy(),
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

// WithRetreat 覆盖断链重注册的退避曲线与/或抖动：任一参数为 nil 表示那一半
// 保持默认（曲线 1s 起翻倍、截到 5min；抖动 FullJitter，见 defaultRetreatPolicy）。
//
// 「只覆盖一半」是契约侧需要的形状：`registry.etcd.backoff` 的 jitter 未写时
// 落下来的是 nil 抖动，此时曲线换了但风暴打散方式不换。
//
// 曲线自身不设上界时（ExponentialBackoff.Max=0）等待会随次数无上界增长——
// 高次数下乘出负 duration，退避直接反转成忙轮询。契约段不允许那种形状
// （非 FIXED 曲线必填 max），代码注入路径也请自带 Max。
func WithRetreat(b retry.Backoff, j retry.Jitter) Option {
	return func(o *options) {
		if b != nil {
			o.retreat.backoff = b
		}
		if j != nil {
			o.retreat.jitter = j
		}
	}
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
