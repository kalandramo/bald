// Package etcd 提供 etcd 配置源：KV 读取 + 原生 watch 推送。
package etcd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/kalandramo/bald/bconfig"
)

var (
	_ bconfig.Reader       = (*Config)(nil)
	_ bconfig.ValueWatcher = (*Config)(nil)
	_ bconfig.Closer       = (*Config)(nil)
)

// DefaultDialTimeout 自建 client 的默认建连超时。
const DefaultDialTimeout = 5 * time.Second

// Config 是 etcd 配置源。
//
// 双模式构造：
//   - [New]：从 options 自建 client（契约装配路径），首次 Load/WatchValue 惰性建连，
//     Close 时释放自建 client；
//   - [NewWithClient]：注入已有 client（复用注册发现等场景的既有连接），
//     本源不负责关闭。
type Config struct {
	opts   options
	client *clientv3.Client
	owned  bool // client 是否由本源自建；仅自建 client 在 Close 时释放
}

// New 创建自建模式的 etcd 配置源（endpoints 必填）。
func New(opts ...Option) (*Config, error) {
	o := options{dialTimeout: DefaultDialTimeout}
	for _, opt := range opts {
		opt(&o)
	}
	if len(o.endpoints) == 0 {
		return nil, errors.New("etcd: endpoints is required")
	}
	if o.path == "" {
		return nil, errors.New("etcd: path is required")
	}
	return &Config{opts: o, owned: true}, nil
}

// NewWithClient 创建注入模式的 etcd 配置源（path 必填）。
func NewWithClient(c *clientv3.Client, opts ...Option) (*Config, error) {
	if c == nil {
		return nil, errors.New("etcd: client is nil")
	}
	o := options{dialTimeout: DefaultDialTimeout}
	for _, opt := range opts {
		opt(&o)
	}
	if o.path == "" {
		return nil, errors.New("etcd: path is required")
	}
	return &Config{opts: o, client: c}, nil
}

// init 惰性建连（仅自建模式）。
func (c *Config) init() error {
	if c.client != nil {
		return nil
	}
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   c.opts.endpoints,
		Username:    c.opts.username,
		Password:    c.opts.password,
		DialTimeout: c.opts.dialTimeout,
		Context:     c.opts.ctx,
	})
	if err != nil {
		return fmt.Errorf("etcd: new client: %w", err)
	}
	c.client = client
	return nil
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
func (c *Config) Load(ctx context.Context, key string) ([]byte, error) {
	if err := c.init(); err != nil {
		return nil, err
	}

	path := c.resolveKey(key)

	var opts []clientv3.OpOption
	if c.opts.prefix {
		opts = append(opts, clientv3.WithPrefix())
	}

	rsp, err := c.client.Get(ctx, path, opts...)
	if err != nil {
		return nil, wrapConnError("get key", path, err)
	}
	if len(rsp.Kvs) == 0 {
		return nil, nil
	}
	return rsp.Kvs[0].Value, nil
}

// WatchValue 实现 [bconfig.ValueWatcher]：键（或默认 path）下数据变更时
// 推送新值；ctx 取消或 watch 流结束后通道关闭。
func (c *Config) WatchValue(ctx context.Context, key string) (<-chan []byte, error) {
	if err := c.init(); err != nil {
		return nil, err
	}

	path := c.resolveKey(key)

	// 短超时探测，确认 etcd 可达（fail-fast）。
	probeCtx, probeCancel := context.WithTimeout(ctx, 2*time.Second)
	defer probeCancel()
	if _, err := c.client.Get(probeCtx, path, clientv3.WithLimit(1)); err != nil {
		return nil, wrapConnError("create watcher", path, err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	var opts []clientv3.OpOption
	if c.opts.prefix {
		opts = append(opts, clientv3.WithPrefix())
	}
	etcdCh := c.client.Watch(watchCtx, path, opts...)

	out := make(chan []byte, 1)
	go func() {
		defer close(out)
		defer cancel()
		for resp := range etcdCh {
			if err := resp.Err(); err != nil {
				return
			}
			for _, ev := range resp.Events {
				if ev.Type == clientv3.EventTypePut {
					select {
					case out <- ev.Kv.Value:
					case <-watchCtx.Done():
						return
					}
				}
			}
		}
	}()
	return out, nil
}

// Close 释放自建 client；注入模式为空操作。
func (c *Config) Close() error {
	if c.owned && c.client != nil {
		return c.client.Close()
	}
	return nil
}

// isConnError 判断 err 是否为连接/网络类问题。
func isConnError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	e := strings.ToLower(err.Error())
	indicators := []string{
		"connection refused", "connection reset", "no available endpoints",
		"transport is closing", "i/o timeout", "timeout", "connection timed out",
		"tls:", "connection reset by peer", "eof",
	}
	for _, sub := range indicators {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

// wrapConnError 为连接类错误附加可读前缀，便于定位「配置中心不可达」。
func wrapConnError(op string, path string, err error) error {
	if err == nil {
		return nil
	}
	if isConnError(err) {
		if path == "" {
			return fmt.Errorf("etcd: %s failed (cannot reach etcd server): %w", op, err)
		}
		return fmt.Errorf("etcd: %s failed for path %s (cannot reach etcd server): %w", op, path, err)
	}
	return err
}
