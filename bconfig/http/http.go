// Package http 提供 HTTP 配置源：URL 拉取 + ETag 条件轮询。
package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kalandramo/bald/bconfig"
)

var (
	_ bconfig.Reader       = (*Config)(nil)
	_ bconfig.ValueWatcher = (*Config)(nil)
	_ bconfig.Closer       = (*Config)(nil)
)

// DefaultPollInterval 轮询默认间隔。
const DefaultPollInterval = 30 * time.Second

// DefaultTimeout 自建 client 的默认请求超时。
const DefaultTimeout = 10 * time.Second

// Config 是 HTTP 配置源。
//
// 双模式构造：
//   - [New]：自建 *http.Client（契约装配路径），Close 时关闭空闲连接；
//   - [NewWithClient]：注入已有 client，本源不负责关闭。
type Config struct {
	opts   options
	client *http.Client
	owned  bool
}

// New 创建自建模式的 HTTP 配置源（url 必填）。
func New(opts ...Option) (*Config, error) {
	o := options{method: http.MethodGet, pollInterval: DefaultPollInterval}
	for _, opt := range opts {
		opt(&o)
	}
	if o.url == "" {
		return nil, errors.New("http: url is required")
	}

	client := o.httpClient
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
		o.owned = true
	}

	return &Config{opts: o, client: client, owned: o.owned}, nil
}

// NewWithClient 创建注入模式的 HTTP 配置源（url 必填）。
func NewWithClient(c *http.Client, opts ...Option) (*Config, error) {
	if c == nil {
		return nil, errors.New("http: client is nil")
	}
	o := options{method: http.MethodGet, pollInterval: DefaultPollInterval}
	for _, opt := range opts {
		opt(&o)
	}
	if o.url == "" {
		return nil, errors.New("http: url is required")
	}
	return &Config{opts: o, client: c}, nil
}

// resolveKey 返回实际请求的 URL：key 非空时覆盖配置的默认 url。
func (c *Config) resolveKey(key string) string {
	if key != "" {
		return key
	}
	return c.opts.url
}

// buildRequest 构造带 headers 与可选 If-None-Match 的请求。
func (c *Config) buildRequest(ctx context.Context, url, etag string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, c.opts.method, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range c.opts.headers {
		req.Header.Set(k, v)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	return req, nil
}

// Load 实现 [bconfig.Reader]：请求 URL 返回响应体；304 返回 nil, nil。
func (c *Config) Load(ctx context.Context, key string) ([]byte, error) {
	url := c.resolveKey(key)

	req, err := c.buildRequest(ctx, url, "")
	if err != nil {
		return nil, fmt.Errorf("http: create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: request %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http: %s: %s", url, resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("http: read response body: %w", err)
	}
	return data, nil
}

// WatchValue 实现 [bconfig.ValueWatcher]：HTTP 无推送能力，用 ETag 条件
// GET（If-None-Match）轮询；初始值立即推送，304 视为无变化。
// ctx 取消后通道关闭。
func (c *Config) WatchValue(ctx context.Context, key string) (<-chan []byte, error) {
	url := c.resolveKey(key)

	out := make(chan []byte, 1)

	go func() {
		defer close(out)

		ticker := time.NewTicker(c.opts.pollInterval)
		defer ticker.Stop()

		var lastETag string

		// 初始拉取：立即推送当前值（失败静默，等下个轮询周期重试）。
		data, etag, err := c.fetchWithETag(ctx, url, "")
		if err == nil {
			lastETag = etag
			if data != nil {
				select {
				case out <- data:
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
				data, etag, err := c.fetchWithETag(ctx, url, lastETag)
				if err != nil {
					continue
				}
				if etag != "" {
					lastETag = etag
				}
				if data == nil {
					continue // 304，无变化
				}
				select {
				case out <- data:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, nil
}

// fetchWithETag 执行条件 GET，返回 body/ETag；304 时 body 为 nil。
func (c *Config) fetchWithETag(ctx context.Context, url, etag string) (data []byte, respETag string, err error) {
	req, err := c.buildRequest(ctx, url, etag)
	if err != nil {
		return nil, "", err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	respETag = resp.Header.Get("ETag")

	if resp.StatusCode == http.StatusNotModified {
		return nil, respETag, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, respETag, fmt.Errorf("http: %s: %s", url, resp.Status)
	}

	data, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, respETag, fmt.Errorf("http: read response body: %w", err)
	}
	return data, respETag, nil
}

// Close 关闭自建 client 的空闲连接；注入模式为空操作。
func (c *Config) Close() error {
	if c.owned {
		c.client.CloseIdleConnections()
	}
	return nil
}
