// Package fs 提供 go:embed 内嵌文件系统配置源（静态读取，无 watch）。
//
// fsys 是编译期资源（embed.FS），无法经配置契约表达——本源定位为代码级 API，
// 不参与 bootstrap 契约装配。
package fs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/kalandramo/bald/bconfig"
)

var _ bconfig.Reader = (*Config)(nil)

// Config 是内嵌文件系统配置源。
type Config struct {
	fsys fs.FS
	path string
}

// New 创建内嵌文件系统配置源（fsys 必填，path 为默认读取路径）。
func New(fsys fs.FS, opts ...Option) (*Config, error) {
	if fsys == nil {
		return nil, errors.New("fs: fsys is required (an io/fs.FS must be provided)")
	}
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}
	return &Config{fsys: fsys, path: o.path}, nil
}

// resolvePath 返回实际读取的路径：key 非空时覆盖配置的默认 path。
func (c *Config) resolvePath(key string) string {
	if key != "" {
		return key
	}
	return c.path
}

// Load 实现 [bconfig.Reader]：返回内嵌文件的原始内容。
func (c *Config) Load(_ context.Context, key string) ([]byte, error) {
	path := c.resolvePath(key)
	if path == "" {
		return nil, errors.New("fs: no file path specified")
	}
	data, err := fs.ReadFile(c.fsys, path)
	if err != nil {
		return nil, fmt.Errorf("fs: read embedded file %s: %w", path, err)
	}
	return data, nil
}
