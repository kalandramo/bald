package env

import (
	"context"
	"errors"

	"github.com/kalandramo/bald/bconfig"
)

var _ bconfig.Reader = (*source)(nil)

type source struct {
	options *options
}

// New creates an environment variable config source.
// The key option is used as the default environment variable name.
func New(opts ...Option) (*source, error) {
	o := &options{
		prefix: "",
		key:    "",
	}
	for _, opt := range opts {
		opt(o)
	}

	return &source{options: o}, nil
}

// resolveKey returns the environment variable name to look up.
// If key is empty, the configured default key (optionally prefixed) is used.
func (s *source) resolveKey(key string) string {
	if key == "" {
		key = s.options.key
	}
	if s.options.prefix != "" && key != "" {
		key = s.options.prefix + key
	}
	return key
}

// Load implements [baseConfig.Reader].
// It returns the value of the environment variable named by key
// (or the configured default key). Returns (nil, nil) if the
// variable is not set.
func (s *source) Load(_ context.Context, key string) ([]byte, error) {
	envKey := s.resolveKey(key)
	if envKey == "" {
		return nil, errors.New("env: no key specified")
	}

	val, ok := lookupEnv(envKey)
	if !ok {
		return nil, nil
	}
	return []byte(val), nil
}
