package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewHTTPRejectsBadBaseURL(t *testing.T) {
	cases := map[string]string{
		"empty":        "",
		"no scheme":    "localhost:8080",
		"other scheme": "ftp://example.com",
		"no host":      "http://",
	}
	for name, base := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewHTTP(base); err == nil {
				t.Fatalf("NewHTTP(%q) succeeded, want error", base)
			}
		})
	}
}

func TestNewHTTPAppliesOptions(t *testing.T) {
	hc, err := NewHTTP("https://example.com/api/", WithClientTimeout(3*time.Second))
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	if hc.BaseURL != "https://example.com/api" {
		t.Fatalf("BaseURL = %q, want trailing slash trimmed", hc.BaseURL)
	}
	if hc.Timeout != 3*time.Second {
		t.Fatalf("Timeout = %v, want 3s", hc.Timeout)
	}
}

// 自定义 Transport 被真正用上：请求经注入的 RoundTripper 返回固定响应。
func TestNewHTTPUsesInjectedTransport(t *testing.T) {
	tr := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTeapot,
			Body:       io.NopCloser(strings.NewReader("from-stub")),
			Header:     make(http.Header),
		}, nil
	})
	hc, err := NewHTTP("http://example.com", WithTransport(tr))
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	resp, err := hc.Get("http://example.com/ping")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("StatusCode = %d, want 418 (injected transport was bypassed)", resp.StatusCode)
	}
}

// 默认 Transport 下按 BaseURL 发得出请求（真起本地 HTTP 服务）。
func TestNewHTTPReachesServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok:"+r.URL.Path)
	}))
	t.Cleanup(srv.Close)

	hc, err := NewHTTP(srv.URL)
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	resp, err := hc.Get(hc.BaseURL + "/health")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok:/health" {
		t.Fatalf("body = %q, want ok:/health", body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
