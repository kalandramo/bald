package vault

import (
	"context"
	"time"
)

// Option 是 Vault 配置源选项。
type Option func(*options)

type options struct {
	ctx          context.Context
	address      string
	token        string
	path         string
	dataKey      string
	pollInterval time.Duration
}

// WithContext 注入基础 context（预留；nil 时用 context.Background）。
func WithContext(ctx context.Context) Option {
	return func(o *options) {
		o.ctx = ctx
	}
}

// WithAddress 设置 Vault 服务地址（自建模式；空则回退 VAULT_ADDR 环境变量
// 或 https://127.0.0.1:8200）。
func WithAddress(addr string) Option {
	return func(o *options) {
		o.address = addr
	}
}

// WithToken 设置认证 token（自建模式；空则回退 VAULT_TOKEN 环境变量）。
func WithToken(token string) Option {
	return func(o *options) {
		o.token = token
	}
}

// WithPath 设置 secret 路径（必填），如 "secret/data/myapp/config"。
func WithPath(p string) Option {
	return func(o *options) {
		o.path = p
	}
}

// WithDataKey 设置 secret Data 中承载原始配置的字段名，默认 [DefaultDataKey]。
// 复杂 secret 结构可指向含配置载荷的字段（如 "config"/"data"/"value"）。
func WithDataKey(k string) Option {
	return func(o *options) {
		o.dataKey = k
	}
}

// WithPollInterval 设置轮询间隔，默认 [DefaultPollInterval]。
func WithPollInterval(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.pollInterval = d
		}
	}
}
