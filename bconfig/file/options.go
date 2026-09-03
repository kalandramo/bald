package file

import "context"

// Option is file config option.
type Option func(o *options)

type options struct {
	ctx   context.Context
	path  string
	watch bool
}

// WithContext with file config context.
func WithContext(ctx context.Context) Option {
	return func(o *options) {
		o.ctx = ctx
	}
}

// WithPath sets the file path to load configuration from.
func WithPath(p string) Option {
	return func(o *options) {
		o.path = p
	}
}

// WithWatch enables fsnotify-based file watching during New.
// When enabled, the source pre-initialises an fsnotify watcher so that
// WatchValue can be called without lazy initialisation.
func WithWatch(watch bool) Option {
	return func(o *options) {
		o.watch = watch
	}
}
