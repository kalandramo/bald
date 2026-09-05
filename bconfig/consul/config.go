// Package consul 提供 HashiCorp Consul 配置源：KV 读取 + watch plan 推送。
package consul

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/api/watch"

	"github.com/kalandramo/bald/bconfig"
)

var (
	_ bconfig.Reader       = (*Config)(nil)
	_ bconfig.ValueWatcher = (*Config)(nil)
	_ bconfig.Closer       = (*Config)(nil)
)

// DefaultAddress 本地开发默认地址。
const DefaultAddress = "127.0.0.1:8500"

// Config 是 consul 配置源。
//
// 双模式构造：
//   - [New]：从 options 自建 client（契约装配路径）；
//   - [NewWithClient]：注入已有 client，本源不负责关闭。
type Config struct {
	opts   options
	client *api.Client
	owned  bool
}

// New 创建自建模式的 consul 配置源（path 必填；address/token/scheme 缺省时
// 使用 consul 默认值，地址默认 [DefaultAddress]）。
func New(opts ...Option) (*Config, error) {
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}
	if o.path == "" {
		return nil, errors.New("consul: path is required")
	}

	cfg := api.DefaultConfig()
	if o.address != "" {
		cfg.Address = o.address
	}
	if o.token != "" {
		cfg.Token = o.token
	}
	if o.scheme != "" {
		cfg.Scheme = o.scheme
	}
	client, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("consul: new client: %w", err)
	}
	return &Config{opts: o, client: client, owned: true}, nil
}

// NewWithClient 创建注入模式的 consul 配置源（path 必填）。
func NewWithClient(c *api.Client, opts ...Option) (*Config, error) {
	if c == nil {
		return nil, errors.New("consul: client is nil")
	}
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}
	if o.path == "" {
		return nil, errors.New("consul: path is required")
	}
	return &Config{opts: o, client: c}, nil
}

// resolveKey 返回实际读取的键：key 非空时覆盖配置的默认 path。
func (c *Config) resolveKey(key string) string {
	if key != "" {
		return key
	}
	return c.opts.path
}

// Load 实现 [bconfig.Reader]：返回 key（或默认 path）下的原始值；
// 键不存在时返回 nil, nil。
//
// ctx 必须透传给底层 HTTP 请求：否则 Store 层级超时/取消对该源无效
// （配置中心不可达时启动将无限阻塞）。
func (c *Config) Load(ctx context.Context, key string) ([]byte, error) {
	q := (*api.QueryOptions)(nil).WithContext(ctx)
	kv, _, err := c.client.KV().Get(c.resolveKey(key), q)
	if err != nil {
		return nil, err
	}
	if kv == nil {
		return nil, nil
	}
	return kv.Value, nil
}

// WatchValue 实现 [bconfig.ValueWatcher]：键（或默认 path）下数据变更时
// 推送新值；ctx 取消或 watch plan 停止后通道关闭。
func (c *Config) WatchValue(ctx context.Context, key string) (<-chan []byte, error) {
	path := c.resolveKey(key)

	wp, err := watch.Parse(map[string]any{"type": "key", "key": path})
	if err != nil {
		return nil, fmt.Errorf("consul: parse watch plan for %s: %w", path, err)
	}

	out := make(chan []byte, 1)
	wp.Handler = func(_ uint64, data any) {
		// 键删除：watch plan 以 nil 回调。推送空文档让层清空、
		// 回退低优先级层/默认值，而非静默保留最后一次值。
		if data == nil {
			select {
			case out <- nil:
			case <-ctx.Done():
			}
			return
		}
		kvPair, ok := data.(*api.KVPair)
		if !ok || kvPair == nil {
			return
		}
		select {
		case out <- kvPair.Value:
		case <-ctx.Done():
		}
	}

	go func() {
		defer close(out)
		_ = wp.RunWithClientAndHclog(c.client, nil)
	}()

	go func() {
		<-ctx.Done()
		wp.Stop()
	}()

	return out, nil
}

// Close 释放自建 client 资源；注入模式为空操作。
// consul api.Client 本身无可释放连接池，此处仅保证 owned 语义完整。
func (c *Config) Close() error {
	return nil
}
