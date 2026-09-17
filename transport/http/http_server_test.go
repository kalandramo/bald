package httpserver

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// freeAddr 返回一个空闲的本机地址（绑定 :0 试探后关闭）。
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe free addr: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// startAndWait 启动 server 并等待 Endpoint 就绪（:0 场景），返回 stop 函数。
func startAndWait(t *testing.T, srv interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Endpoint() string
}) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()

	deadline := time.After(2 * time.Second)
	for {
		if ep := srv.Endpoint(); ep != "" && !strings.HasSuffix(ep, ":0") && !strings.HasSuffix(ep, "://:0") {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatal("server Endpoint not ready in time")
		case <-time.After(10 * time.Millisecond):
		}
	}
	return func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = srv.Stop(stopCtx)
		cancel()
		<-done
	}
}

// TestHTTPServer_DynamicPort：绑定 ":0" 时 Endpoint 应解析为真实随机端口。
func TestHTTPServer_DynamicPort(t *testing.T) {
	opts := &bootstrapv1.Server_Http{Addr: ":0"}
	srv := NewHTTPServer(opts, http.NewServeMux())
	stop := startAndWait(t, srv)
	defer stop()

	ep := srv.Endpoint()
	if !strings.HasPrefix(ep, "http://") {
		t.Fatalf("scheme = %q, want http://", ep)
	}
	if strings.HasSuffix(ep, ":0") {
		t.Fatalf("Endpoint still :0, dynamic port not resolved: %q", ep)
	}
	addr := strings.TrimPrefix(ep, "http://")
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + addr + "/nonexistent")
	if err != nil {
		t.Fatalf("GET endpoint: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (server reachable)", resp.StatusCode)
	}
}

// TestHTTPServer_HTTPS_Scheme：Tls 段非 nil 时 Endpoint scheme 应为 https。
func TestHTTPServer_HTTPS_Scheme(t *testing.T) {
	tlsSrv := NewHTTPServer(&bootstrapv1.Server_Http{Addr: ":0", Tls: &bootstrapv1.Server_TLS{}}, http.NewServeMux())
	tlsStop := startAndWait(t, tlsSrv)
	defer tlsStop()
	if ep := tlsSrv.Endpoint(); !strings.HasPrefix(ep, "https://") {
		t.Fatalf("with TLS, scheme = %q, want https://", ep)
	}

	plain := NewHTTPServer(&bootstrapv1.Server_Http{Addr: ":0"}, http.NewServeMux())
	plainStop := startAndWait(t, plain)
	defer plainStop()
	if ep := plain.Endpoint(); !strings.HasPrefix(ep, "http://") {
		t.Fatalf("without TLS, scheme = %q, want http://", ep)
	}
}

// TestHTTPServer_NoFrameworkRoutes：协议实现不注册任何框架路由——
// 业务 handler 里没有的路径一律 404（探针归装配层/业务，见设计文档）。
func TestHTTPServer_NoFrameworkRoutes(t *testing.T) {
	srv := NewHTTPServer(&bootstrapv1.Server_Http{Addr: ":0"}, http.NewServeMux())
	stop := startAndWait(t, srv)
	defer stop()

	addr := "http://" + strings.TrimPrefix(srv.Endpoint(), "http://")
	client := &http.Client{Timeout: time.Second}
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := client.Get(addr + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 (transport registers no probes)", path, resp.StatusCode)
		}
	}
}

// TestHTTPServer_AttachHandler：延迟挂载（gateway 路径）应替换根 handler，
// 且不改变监听地址。
func TestHTTPServer_AttachHandler(t *testing.T) {
	srv := NewHTTPServer(&bootstrapv1.Server_Http{Addr: ":0"}, nil)
	stop := startAndWait(t, srv)
	defer stop()

	addr := "http://" + strings.TrimPrefix(srv.Endpoint(), "http://")
	client := &http.Client{Timeout: time.Second}

	resp, err := client.Get(addr + "/readyz")
	if err != nil {
		t.Fatalf("GET before attach: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("nil handler should 404, got %d", resp.StatusCode)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	srv.AttachHandler(mux)

	resp, err = client.Get(addr + "/readyz")
	if err != nil {
		t.Fatalf("GET after attach: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("attached handler not effective: got %d", resp.StatusCode)
	}
}
