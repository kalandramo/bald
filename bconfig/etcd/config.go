// Package etcd 提供 etcd 配置源：KV 读取 + 原生 watch 推送。
package etcd

import (
	"context"
	stdjson "encoding/json"
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
//
// prefix 模式：聚合前缀下全部 KV 为一份嵌套文档（键剥前缀后按点路径展开，
// "/" 亦视作层级分隔），而非字典序首键——单文档源语义要求整份文档。
func (c *Config) Load(ctx context.Context, key string) ([]byte, error) {
	if err := c.init(); err != nil {
		return nil, err
	}

	path := c.resolveKey(key)

	if c.opts.prefix {
		return c.loadPrefix(ctx, path)
	}

	rsp, err := c.client.Get(ctx, path)
	if err != nil {
		return nil, wrapConnError("get key", path, err)
	}
	if len(rsp.Kvs) == 0 {
		return nil, nil
	}
	return rsp.Kvs[0].Value, nil
}

// loadPrefix 聚合前缀下全部 KV 为嵌套 JSON 文档：相对键按点路径展开
// （与框架点路径键约定一致，"/" 先归一为 "."）；值一律为字符串，
// 类型规范化交由下游 bconf.UnmarshalMap。
func (c *Config) loadPrefix(ctx context.Context, path string) ([]byte, error) {
	rsp, err := c.client.Get(ctx, path, clientv3.WithPrefix())
	if err != nil {
		return nil, wrapConnError("get prefix", path, err)
	}
	doc := map[string]any{}
	for _, kv := range rsp.Kvs {
		rel := strings.TrimPrefix(string(kv.Key), path)
		rel = strings.TrimPrefix(rel, "/")
		expandKey(strings.ReplaceAll(rel, "/", "."), string(kv.Value), doc)
	}
	return stdjson.Marshal(doc)
}

// expandKey 把点路径值写入嵌套 map（"server.http.addr" → {"server":{"http":{"addr":...}}}）。
func expandKey(path string, value any, m map[string]any) {
	parts := strings.Split(path, ".")
	cur := m
	for i, p := range parts {
		if p == "" {
			continue // 防御空段（前缀尾部分隔符）
		}
		if i == len(parts)-1 {
			cur[p] = value
			return
		}
		sub, ok := cur[p].(map[string]any)
		if !ok {
			sub = map[string]any{}
			cur[p] = sub
		}
		cur = sub
	}
}

// WatchValue 实现 [bconfig.ValueWatcher]：键（或默认 path）下数据变更时
// 推送新值；ctx 取消或 watch 流结束后通道关闭。
//
// 单键模式：PUT 推新值；DELETE 推 nil（层清空，回退低优先级层/默认值）。
// prefix 模式：任一 KV 变更（含 DELETE）后回读前缀全量文档再推送——
// 整文档语义下增量推送无意义，回读天然覆盖删除。
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

	out := make(chan []byte, 1)

	if c.opts.prefix {
		etcdCh := c.client.Watch(watchCtx, path, clientv3.WithPrefix())
		go func() {
			defer close(out)
			defer cancel()
			// 初始全量推送一次（对齐 http 源语义；Store 已 Load 过则幂等）。
			if doc, err := c.loadPrefix(watchCtx, path); err == nil {
				select {
				case out <- doc:
				case <-watchCtx.Done():
					return
				}
			}
			for resp := range etcdCh {
				if err := resp.Err(); err != nil {
					return
				}
				// 任意 PUT/DELETE：回读前缀全量（天然覆盖删除语义）。
				doc, err := c.loadPrefix(watchCtx, path)
				if err != nil {
					continue
				}
				select {
				case out <- doc:
				case <-watchCtx.Done():
					return
				}
			}
		}()
		return out, nil
	}

	etcdCh := c.client.Watch(watchCtx, path)
	go func() {
		defer close(out)
		defer cancel()
		for resp := range etcdCh {
			if err := resp.Err(); err != nil {
				return
			}
			for _, ev := range resp.Events {
				var data []byte
				switch ev.Type {
				case clientv3.EventTypePut:
					data = ev.Kv.Value
				case clientv3.EventTypeDelete:
					data = nil // 键删除：推送空文档让层清空、回退低优先级层
				}
				select {
				case out <- data:
				case <-watchCtx.Done():
					return
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
