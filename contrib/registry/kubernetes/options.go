package kubernetes

// options 汇集 kubernetes Registry 的可调参数。
type options struct {
	namespace string
}

// Option 是 kubernetes Registry 的函数式选项。
type Option func(o *options)

func newOptions(opts ...Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithNamespace 设置 informer 监听的 namespace（空 = 全部）。
func WithNamespace(ns string) Option {
	return func(o *options) { o.namespace = ns }
}
