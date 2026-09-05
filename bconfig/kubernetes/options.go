package kubernetes

// Option is kubernetes option.
type Option func(*options)

type options struct {
	// kubernetes namespace
	Namespace string
	// ConfigMapName 契约装配的默认 ConfigMap 名（key 为空时使用）
	ConfigMapName string
	// DataKey 契约装配的默认 data key（key 为空时使用；空则合并全部 data）
	DataKey string
	// kubernetes labelSelector example `app=test`
	LabelSelector string
	// kubernetes fieldSelector example `app=test`
	FieldSelector string
	// set KubeConfig out-of-cluster Use outside cluster
	KubeConfig string
	// set master url
	Master string
}

// WithNamespace with kubernetes namespace.
func WithNamespace(ns string) Option {
	return func(o *options) {
		o.Namespace = ns
	}
}

// WithConfigMapName 设置默认 ConfigMap 名（Load/Watch 以空 key 调用时使用）。
func WithConfigMapName(name string) Option {
	return func(o *options) {
		o.ConfigMapName = name
	}
}

// WithDataKey 设置默认 data key（空 key 装配路径使用；空值合并全部 data 条目）。
func WithDataKey(k string) Option {
	return func(o *options) {
		o.DataKey = k
	}
}

// WithLabelSelector with kubernetes label selector.
func WithLabelSelector(label string) Option {
	return func(o *options) {
		o.LabelSelector = label
	}
}

// WithFieldSelector with kubernetes field selector.
func WithFieldSelector(field string) Option {
	return func(o *options) {
		o.FieldSelector = field
	}
}

// WithKubeConfig with kubernetes config.
func WithKubeConfig(config string) Option {
	return func(o *options) {
		o.KubeConfig = config
	}
}

// WithMaster with kubernetes master.
func WithMaster(master string) Option {
	return func(o *options) {
		o.Master = master
	}
}
