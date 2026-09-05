package etcd

import (
	"context"
	"time"
)

// Option 是 etcd 配置源选项。
type Option func(*options)

type options struct {
	ctx         context.Context
	endpoints   []string
	username    string
	password    string
	path        string
	prefix      bool
	dialTimeout time.Duration
}

// WithContext 注入基础 context（自建 client 使用；nil 时用 context.Background）。
func WithContext(ctx context.Context) Option {
	return func(o *options) {
		o.ctx = ctx
	}
}

// WithEndpoints 设置 etcd 集群地址（自建模式必填）。
func WithEndpoints(addrs ...string) Option {
	return func(o *options) {
		o.endpoints = addrs
	}
}

// WithUsername 设置认证用户名（自建模式）。
func WithUsername(u string) Option {
	return func(o *options) {
		o.username = u
	}
}

// WithPassword 设置认证密码（自建模式）。
func WithPassword(p string) Option {
	return func(o *options) {
		o.password = p
	}
}

// WithPath 设置配置键路径（Load/WatchValue 的默认 key）。
func WithPath(p string) Option {
	return func(o *options) {
		o.path = p
	}
}

// WithPrefix 启用前缀模式（读取/监听一个目录而非单个键）。
func WithPrefix(prefix bool) Option {
	return func(o *options) {
		o.prefix = prefix
	}
}

// WithDialTimeout 设置自建 client 的建连超时，默认 [DefaultDialTimeout]。
func WithDialTimeout(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.dialTimeout = d
		}
	}
}
