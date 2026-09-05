package nacos

const (
	DefaultGroup  = "DEFAULT_GROUP"
	DefaultDataID = "bootstrap.yaml"
)

// Option 是 nacos 配置源选项。
type Option func(*options)

type options struct {
	serverAddrs []string
	namespace   string
	group       string
	dataID      string
}

// WithServerAddrs 设置 nacos 集群地址（自建模式必填），如 "nacos:8848"。
func WithServerAddrs(addrs ...string) Option {
	return func(o *options) {
		o.serverAddrs = addrs
	}
}

// WithNamespace 设置命名空间（自建模式；注入模式下由 client 决定）。
func WithNamespace(ns string) Option {
	return func(o *options) {
		o.namespace = ns
	}
}

// WithGroup 设置配置分组（默认 [DefaultGroup]）。
func WithGroup(group string) Option {
	return func(o *options) {
		o.group = group
	}
}

// WithDataID 设置配置 data ID（必填）。
func WithDataID(dataID string) Option {
	return func(o *options) {
		o.dataID = dataID
	}
}
