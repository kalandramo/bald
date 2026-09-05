package consul

// Option 是 consul 配置源选项。
type Option func(*options)

type options struct {
	address string
	token   string
	scheme  string
	path    string
}

// WithAddress 设置 consul 地址（如 "consul:8500"；自建模式）。
func WithAddress(addr string) Option {
	return func(o *options) {
		o.address = addr
	}
}

// WithToken 设置 ACL token（自建模式）。
func WithToken(token string) Option {
	return func(o *options) {
		o.token = token
	}
}

// WithScheme 设置协议（http/https；自建模式）。
func WithScheme(scheme string) Option {
	return func(o *options) {
		o.scheme = scheme
	}
}

// WithPath 设置配置键路径（Load/WatchValue 的默认 key）。
func WithPath(p string) Option {
	return func(o *options) {
		o.path = p
	}
}
