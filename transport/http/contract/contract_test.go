package contract

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport"
	httpserver "github.com/kalandramo/bald/transport/http"
)

// waitEndpoint 等待 server 解析出真实监听地址（:0 动态端口）。
func waitEndpoint(t *testing.T, srv transport.Server) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for srv.Endpoint() == "" || strings.HasSuffix(srv.Endpoint(), ":0") {
		select {
		case <-deadline:
			t.Fatal("endpoint not ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestProvider_PlainHandler 纯 HTTP 模式：注入 handler 即可启动；
// 协议实现不注册任何框架路由（探针归装配层/业务）。
func TestProvider_PlainHandler(t *testing.T) {
	cfg := &bootstrapv1.Server{
		Http: &bootstrapv1.Server_Http{Addr: ":0"},
	}
	srv, closer, err := Provider(WithHandler(http.NewServeMux()))(context.Background(), cfg)
	if err != nil || srv == nil {
		t.Fatalf("provider = (%v, %v), want non-nil server", srv, err)
	}
	if closer != nil {
		defer closer()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	defer func() {
		// 走 Stop 优雅停机（Start 阻塞在 Serve，ctx 取消不会让它返回）。
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Stop(stopCtx)
		stopCancel()
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Start did not return after Stop")
		}
	}()

	waitEndpoint(t, srv)
	client := &http.Client{Timeout: time.Second}
	addr := "http://" + strings.TrimPrefix(srv.Endpoint(), "http://")
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := client.Get(addr + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 (no framework routes in transport)", path, resp.StatusCode)
		}
	}
}

// TestProvider_Unconfigured 契约无 Http 段 → nil server（引擎跳过该产物）。
func TestProvider_Unconfigured(t *testing.T) {
	srv, closer, err := Provider(WithHandler(http.NewServeMux()))(context.Background(), &bootstrapv1.Server{})
	if err != nil || srv != nil || closer != nil {
		t.Fatalf("unconfigured = (srv=%v, closer!=nil: %t, err=%v), want all nil", srv, closer != nil, err)
	}
}

// TestProvider_IgnoresGatewayDriver 钉住本包的边界：driver 归转码面
// Provider 消费，本包只按 handler 构 Plain HTTP 面。
func TestProvider_IgnoresGatewayDriver(t *testing.T) {
	// server.http.driver 写什么都不改变产物（转码拓扑见 transport/gateway/contract）。
	cfg := &bootstrapv1.Server{
		Http: &bootstrapv1.Server_Http{Addr: ":0", Driver: "grpc-gateway"},
		Grpc: &bootstrapv1.Server_Grpc{Addr: "127.0.0.1:0"},
	}
	srv, closer, err := Provider(WithHandler(http.NewServeMux()))(context.Background(), cfg)
	if err != nil || srv == nil {
		t.Fatalf("provider = (%v, %v), want plain HTTP server", srv, err)
	}
	if closer != nil {
		defer closer()
	}
	if _, ok := srv.(*httpserver.HTTPServer); !ok {
		t.Fatalf("server type = %T, want *httpserver.HTTPServer (driver is not consumed here)", srv)
	}
}
