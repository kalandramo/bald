// Package apollo 提供携程 Apollo 配置源：KV 读取 + 原生变更推送。
package apollo

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/apolloconfig/agollo/v4"
	apolloconfig "github.com/apolloconfig/agollo/v4/env/config"

	"github.com/kalandramo/bald/bconfig"
)

var (
	_ bconfig.Reader       = (*Config)(nil)
	_ bconfig.ValueWatcher = (*Config)(nil)
)

// Config 是 Apollo 配置源。
//
// 双模式构造：
//   - [New]：从 options 自建 agollo client（契约装配路径），启动即连接；
//   - [NewWithClient]：注入已有 agollo.Client，本源不负责关闭。
type Config struct {
	opts   options
	client agollo.Client
	owned  bool
}

// New 创建自建模式的 Apollo 配置源（appid/endpoint 必填）。
// 与 go-wind 版不同：连接失败返回 error，不 panic。
func New(opts ...Option) (*Config, error) {
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}
	if o.appid == "" {
		return nil, errors.New("apollo: appid is required")
	}
	if o.endpoint == "" {
		return nil, errors.New("apollo: endpoint is required")
	}
	if o.namespace == "" {
		return nil, errors.New("apollo: namespace is required")
	}

	client, err := agollo.StartWithConfig(func() (*apolloconfig.AppConfig, error) {
		return &apolloconfig.AppConfig{
			AppID:            o.appid,
			Cluster:          o.cluster,
			NamespaceName:    o.namespace,
			IP:               o.endpoint,
			IsBackupConfig:   o.isBackupConfig,
			Secret:           o.secret,
			BackupConfigPath: o.backupPath,
		}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("apollo: start client: %w", err)
	}
	return &Config{opts: o, client: client, owned: true}, nil
}

// NewWithClient 创建注入模式的 Apollo 配置源（namespace 必填）。
func NewWithClient(c agollo.Client, opts ...Option) (*Config, error) {
	if c == nil {
		return nil, errors.New("apollo: client is nil")
	}
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}
	if o.namespace == "" {
		return nil, errors.New("apollo: namespace is required")
	}
	return &Config{opts: o, client: c}, nil
}

// resolveNamespace 返回实际读取的命名空间：key 非空时覆盖配置的默认 namespace。
func (c *Config) resolveNamespace(key string) string {
	if key != "" {
		return key
	}
	return c.opts.namespace
}

// getConfig 把命名空间缓存里的扁平 KV 展开为嵌套 JSON 文档。
func (c *Config) getConfig(ns string) ([]byte, error) {
	next := map[string]any{}
	c.client.GetConfigCache(ns).Range(func(key, value any) bool {
		resolve(genKey(ns, key.(string)), value, next)
		return true
	})
	return stdjson.Marshal(next)
}

// getOriginConfig 返回结构化命名空间（yaml/yml/json）的原始文档内容。
func (c *Config) getOriginConfig(ns string) ([]byte, error) {
	value, err := c.client.GetConfigCache(ns).Get("content")
	if err != nil {
		return nil, err
	}
	s, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("apollo: namespace %s content is not a string", ns)
	}
	return []byte(s), nil
}

// Load 实现 [bconfig.Reader]：返回命名空间对应的配置文档。
// 结构化命名空间（yaml/yml/json）在 WithOriginalConfig 模式下返回原始文档，
// 否则（含 properties）把扁平 KV 展开为嵌套 JSON。
func (c *Config) Load(_ context.Context, key string) ([]byte, error) {
	ns := c.resolveNamespace(key)

	if c.opts.originConfig && strings.Contains(ns, ".") &&
		!strings.HasSuffix(ns, "."+properties) &&
		(format(ns) == yaml || format(ns) == yml || format(ns) == json) {
		return c.getOriginConfig(ns)
	}
	return c.getConfig(ns)
}

// WatchValue 实现 [bconfig.ValueWatcher]：命名空间配置变更时推送新文档；
// ctx 取消后通道关闭并反注册监听器。
func (c *Config) WatchValue(ctx context.Context, key string) (<-chan []byte, error) {
	ns := c.resolveNamespace(key)
	return newWatchValueChannel(ctx, c, ns)
}
