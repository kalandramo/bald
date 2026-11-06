package client

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPClient 是出站 HTTP 客户端与基地址的组合。
//
// 内嵌 *http.Client，可直接当作标准库客户端使用（传给任意 SDK）；BaseURL 已
// 去掉末尾 "/"，拼接路径时不会出现双斜杠。
type HTTPClient struct {
	*http.Client
	BaseURL string
}

// HTTPOption 配置 NewHTTP 的行为。
type HTTPOption func(*httpConfig)

type httpConfig struct {
	timeout   time.Duration
	transport http.RoundTripper
}

// WithClientTimeout 设置单次请求（连接 + 重定向 + 读完响应体）的上限。
// 0 表示不设置。
func WithClientTimeout(d time.Duration) HTTPOption {
	return func(c *httpConfig) { c.timeout = d }
}

// WithTransport 替换底层 RoundTripper（TLS、代理、测试桩都走这条线）。
// 未设置时沿用 http.DefaultTransport。
func WithTransport(rt http.RoundTripper) HTTPOption {
	return func(c *httpConfig) { c.transport = rt }
}

// NewHTTP 按基地址构造出站 HTTP 客户端。
//
// 基地址必须是带主机名的 http(s) URL：其余形状（空、无 scheme、别的 scheme、
// 无主机）都是配置错误——HTTP 客户端没有「猜一个默认地址」的余地。
// 构造不拨号，地址不通只会让第一次请求失败并由调用方重试。
func NewHTTP(baseURL string, opts ...HTTPOption) (*HTTPClient, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("client: http base url is empty")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("client: http base url %q: %w", baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("client: http base url %q must use http or https", baseURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("client: http base url %q has no host", baseURL)
	}

	var c httpConfig
	for _, o := range opts {
		o(&c)
	}
	hc := &http.Client{}
	if c.transport != nil {
		hc.Transport = c.transport
	}
	if c.timeout > 0 {
		hc.Timeout = c.timeout
	}
	return &HTTPClient{Client: hc, BaseURL: strings.TrimSuffix(baseURL, "/")}, nil
}
