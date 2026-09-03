package env

// Option is env config option.
type Option func(o *options)

type options struct {
	prefix string
	key    string
}

// WithPrefix sets a prefix that is prepended to all environment variable
// names (e.g. "APP_" turns "DATABASE_URL" into "APP_DATABASE_URL").
func WithPrefix(p string) Option {
	return func(o *options) {
		o.prefix = p
	}
}

// WithKey sets the default environment variable name used when Load is
// called with an empty key.
func WithKey(k string) Option {
	return func(o *options) {
		o.key = k
	}
}
