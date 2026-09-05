package http

import (
	"net/http"
	"time"
)

// Option 是 HTTP 配置源选项。
type Option func(*options)

type options struct {
	url          string
	method       string
	headers      map[string]string
	httpClient   *http.Client
	pollInterval time.Duration
	owned        bool
}

// WithURL 设置配置端点 URL（必填）。
func WithURL(u string) Option {
	return func(o *options) {
		o.url = u
	}
}

// WithMethod 设置 HTTP 方法（默认 GET）。
func WithMethod(m string) Option {
	return func(o *options) {
		o.method = m
	}
}

// WithHeader 为每个请求附加自定义 header。
func WithHeader(key, value string) Option {
	return func(o *options) {
		if o.headers == nil {
			o.headers = make(map[string]string)
		}
		o.headers[key] = value
	}
}

// WithHTTPClient 注入自定义 client（TLS/proxy 等）；未设置时自建默认 client。
func WithHTTPClient(c *http.Client) Option {
	return func(o *options) {
		o.httpClient = c
	}
}

// WithPollInterval 设置轮询间隔（ETag 条件 GET），默认 [DefaultPollInterval]。
func WithPollInterval(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.pollInterval = d
		}
	}
}
