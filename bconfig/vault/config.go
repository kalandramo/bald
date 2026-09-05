// Package vault 提供 HashiCorp Vault 配置源（KV v1/v2）：secret 读取 + 轮询。
package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/kalandramo/bald/bconfig"
)

var (
	_ bconfig.Reader       = (*Config)(nil)
	_ bconfig.ValueWatcher = (*Config)(nil)
)

// DefaultPollInterval 轮询默认间隔（Vault 无原生推送能力）。
const DefaultPollInterval = 30 * time.Second

// DefaultDataKey 保存原始配置内容的默认字段名。
const DefaultDataKey = "content"

// Config 是 Vault 配置源。
//
// 双模式构造：
//   - [New]：从 options 自建 client（契约装配路径；address/token 缺省时回退
//     VAULT_ADDR/VAULT_TOKEN 环境变量或本机默认值，与 vault 生态惯例一致）；
//   - [NewWithClient]：注入已有 client，本源不负责关闭（vault api.Client 无关闭接口）。
type Config struct {
	opts   options
	client *vaultapi.Client
}

// New 创建自建模式的 Vault 配置源（path 必填）。
func New(opts ...Option) (*Config, error) {
	o := options{dataKey: DefaultDataKey, pollInterval: DefaultPollInterval}
	for _, opt := range opts {
		opt(&o)
	}
	if o.path == "" {
		return nil, errors.New("vault: path is required")
	}

	cfg := vaultapi.DefaultConfig()
	if o.address != "" {
		cfg.Address = o.address
	}
	client, err := vaultapi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("vault: new client: %w", err)
	}
	if o.token != "" {
		client.SetToken(o.token)
	}
	return &Config{opts: o, client: client}, nil
}

// NewWithClient 创建注入模式的 Vault 配置源（path 必填）。
func NewWithClient(c *vaultapi.Client, opts ...Option) (*Config, error) {
	if c == nil {
		return nil, errors.New("vault: client is nil")
	}
	o := options{dataKey: DefaultDataKey, pollInterval: DefaultPollInterval}
	for _, opt := range opts {
		opt(&o)
	}
	if o.path == "" {
		return nil, errors.New("vault: path is required")
	}
	return &Config{opts: o, client: c}, nil
}

// resolveKey 返回实际读取的 secret 路径：key 非空时覆盖配置的默认 path。
func (c *Config) resolveKey(key string) string {
	if key != "" {
		return key
	}
	return c.opts.path
}

// Load 实现 [bconfig.Reader]：读取 path 下的 secret 并提取 dataKey 字段
// （默认 "content"）。
func (c *Config) Load(ctx context.Context, key string) ([]byte, error) {
	path := c.resolveKey(key)

	secret, err := c.client.Logical().ReadWithContext(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("vault: read %s: %w", path, err)
	}
	if secret == nil || secret.Data == nil {
		return nil, nil
	}

	return extractValue(secret.Data, c.opts.dataKey), nil
}

// WatchValue 实现 [bconfig.ValueWatcher]：Vault 无原生推送，以轮询模拟；
// 初始值立即推送，内容变化时推送新值。ctx 取消后通道关闭。
func (c *Config) WatchValue(ctx context.Context, key string) (<-chan []byte, error) {
	path := c.resolveKey(key)

	out := make(chan []byte, 1)

	go func() {
		defer close(out)

		ticker := time.NewTicker(c.opts.pollInterval)
		defer ticker.Stop()

		var lastValue []byte

		// 初始拉取：立即推送当前值（失败静默，等下个轮询周期重试）。
		if secret, err := c.client.Logical().ReadWithContext(ctx, path); err == nil &&
			secret != nil && secret.Data != nil {
			val := extractValue(secret.Data, c.opts.dataKey)
			lastValue = val
			if val != nil {
				select {
				case out <- val:
				case <-ctx.Done():
					return
				}
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				secret, err := c.client.Logical().ReadWithContext(ctx, path)
				if err != nil || secret == nil || secret.Data == nil {
					continue
				}
				val := extractValue(secret.Data, c.opts.dataKey)
				if bytesEqual(val, lastValue) {
					continue
				}
				lastValue = val
				select {
				case out <- val:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, nil
}

// extractValue 从 Vault secret 的 Data map 提取原始配置字节。
// KV v2 的 Data 包装为 {"data": {...}, "metadata": {...}}，先剥壳；
// KV v1 的 Data 即扁平 KV。dataKey 缺失时整体 JSON 序列化兜底。
func extractValue(data map[string]any, dataKey string) []byte {
	// KV v2：剥 "data" 包装层。
	if inner, ok := data["data"]; ok {
		if innerMap, ok := inner.(map[string]any); ok {
			data = innerMap
		}
	}

	if val, ok := data[dataKey]; ok {
		switch v := val.(type) {
		case string:
			return []byte(v)
		case []byte:
			return v
		default:
			if b, err := json.Marshal(v); err == nil {
				return b
			}
		}
	}

	if b, err := json.Marshal(data); err == nil {
		return b
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
