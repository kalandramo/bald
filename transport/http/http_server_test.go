package httpserver

import (
	"context"
	"errors"
	"fmt"
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

// getProbe 对运行中 server 的探针路径发起 GET，返回状态码。
func getProbe(t *testing.T, ep, path string) int {
	t.Helper()
	addr := strings.TrimPrefix(ep, "http://")
	resp, err := http.Get("http://" + addr + path) //nolint:gosec // 测试内本地地址
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestHTTPServer_DynamicPort：绑定 ":0" 时 Endpoint 应解析为真实随机端口。
func TestHTTPServer_DynamicPort(t *testing.T) {
	opts := &bootstrapv1.Server_Http{Addr: ":0"}
	srv := NewHTTPServer(opts, http.NewServeMux(), nil)
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
	tlsSrv := NewHTTPServer(&bootstrapv1.Server_Http{Addr: ":0", Tls: &bootstrapv1.Server_TLS{}}, http.NewServeMux(), nil)
	tlsStop := startAndWait(t, tlsSrv)
	defer tlsStop()
	if ep := tlsSrv.Endpoint(); !strings.HasPrefix(ep, "https://") {
		t.Fatalf("with TLS, scheme = %q, want https://", ep)
	}

	plain := NewHTTPServer(&bootstrapv1.Server_Http{Addr: ":0"}, http.NewServeMux(), nil)
	plainStop := startAndWait(t, plain)
	defer plainStop()
	if ep := plain.Endpoint(); !strings.HasPrefix(ep, "http://") {
		t.Fatalf("without TLS, scheme = %q, want http://", ep)
	}
}

// TestHTTPServer_HealthAndReadyz：/healthz 恒 200；/readyz 随 readiness 回调变化。
func TestHTTPServer_HealthAndReadyz(t *testing.T) {
	srv := NewHTTPServer(&bootstrapv1.Server_Http{Addr: ":0"}, http.NewServeMux(), nil)
	stop := startAndWait(t, srv)
	defer stop()

	if code := getProbe(t, srv.Endpoint(), "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", code)
	}
	if code := getProbe(t, srv.Endpoint(), "/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz (nil readiness) = %d, want 200", code)
	}
}

// TestHTTPServer_Readyz_UnreadyReturns503：readiness 返回 error 时 /readyz 返回 503。
func TestHTTPServer_Readyz_UnreadyReturns503(t *testing.T) {
	ready := func(ctx context.Context) error { return fmt.Errorf("db not connected") }
	srv := NewHTTPServer(&bootstrapv1.Server_Http{Addr: ":0"}, http.NewServeMux(), ready)
	stop := startAndWait(t, srv)
	defer stop()

	if code := getProbe(t, srv.Endpoint(), "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200 (liveness independent)", code)
	}
	if code := getProbe(t, srv.Endpoint(), "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz (unready) = %d, want 503", code)
	}
}

// TestHTTPServer_CustomProbePaths WithProbePaths 覆盖探针路径：
// 自定义路径生效（存活 200 / 就绪 503），默认路径不再注册（404）。
func TestHTTPServer_CustomProbePaths(t *testing.T) {
	srv := NewHTTPServer(
		&bootstrapv1.Server_Http{Addr: ":0"},
		http.NewServeMux(),
		func(context.Context) error { return errors.New("not ready") },
		WithProbePaths("/livez", "/ready"),
	)
	stop := startAndWait(t, srv)
	defer stop()

	addr := "http://" + strings.TrimPrefix(srv.Endpoint(), "http://")
	client := &http.Client{Timeout: time.Second}

	status := func(path string) int {
		t.Helper()
		resp, err := client.Get(addr + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := status("/livez"); got != http.StatusOK {
		t.Errorf("custom livez path = %d, want 200", got)
	}
	if got := status("/ready"); got != http.StatusServiceUnavailable {
		t.Errorf("custom ready path = %d, want 503 (readiness failing)", got)
	}
	if got := status("/healthz"); got != http.StatusNotFound {
		t.Errorf("default /healthz should be unregistered (404), got %d", got)
	}
}
