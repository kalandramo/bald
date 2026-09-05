package fs

// Option 是内嵌文件系统配置源选项。
type Option func(*options)

type options struct {
	path string
}

// WithPath 设置默认文件路径（Load 以空 key 调用时使用）。
func WithPath(p string) Option {
	return func(o *options) {
		o.path = p
	}
}
