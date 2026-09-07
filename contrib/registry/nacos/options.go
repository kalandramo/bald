package nacos

import "time"

type options struct {
	serverAddrs []string
	namespace   string
	prefix      string
	weight      float64
	cluster     string
	group       string
	kind        string
	timeout     time.Duration
	username    string
	password    string
}

// Option 是 nacos Registry 的函数式选项。
type Option func(o *options)

func newOptions(opts ...Option) options {
	o := options{
		prefix:  "/microservices",
		cluster: "DEFAULT",
		group:   "DEFAULT_GROUP",
		weight:  100,
		kind:    "grpc",
		timeout: 10 * time.Second,
	}
	for _, option := range opts {
		option(&o)
	}
	return o
}

// WithServerAddrs 设置 nacos server 地址（host:port 列表，自建 client 必填）。
func WithServerAddrs(addrs ...string) Option {
	return func(o *options) { o.serverAddrs = addrs }
}

// WithNamespace 设置 nacos namespace。
func WithNamespace(ns string) Option {
	return func(o *options) { o.namespace = ns }
}

// WithPrefix 设置前缀（预留，对齐契约 prefix 字段）。
func WithPrefix(prefix string) Option {
	return func(o *options) { o.prefix = prefix }
}

// WithWeight 设置实例默认权重。
func WithWeight(weight float64) Option {
	return func(o *options) { o.weight = weight }
}

// WithCluster 设置集群名。
func WithCluster(cluster string) Option {
	return func(o *options) { o.cluster = cluster }
}

// WithGroup 设置分组名。
func WithGroup(group string) Option {
	return func(o *options) { o.group = group }
}

// WithDefaultKind 设置实例默认协议类型（如 "grpc"）。
func WithDefaultKind(kind string) Option {
	return func(o *options) { o.kind = kind }
}

// WithTimeout 设置 client 请求超时。
func WithTimeout(d time.Duration) Option {
	return func(o *options) { o.timeout = d }
}

// WithUsername 设置鉴权用户名（服务端开启 auth 时必填）。
func WithUsername(username string) Option {
	return func(o *options) { o.username = username }
}

// WithPassword 设置鉴权密码。
func WithPassword(password string) Option {
	return func(o *options) { o.password = password }
}
