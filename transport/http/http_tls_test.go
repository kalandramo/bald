package httpserver

// HTTPS 用例：证明契约 server.http.tls 真的生效——握手成功、明文被拒、
// 配不出证书时启动即失败（不静默退回明文）。证书材料来自 bconf/tlsconf/tlstest。

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconf/tlsconf"
	"github.com/kalandramo/bald/bconf/tlsconf/tlstest"
)

// startTLS 启动带 TLS 契约的 server，返回停止函数；Start 的错误在这里必须可见
// （异步吞错的启动会让「服务没起来」看起来像起来了）。
func startTLS(t *testing.T, srv *HTTPServer) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- srv.Start(ctx) }()

	deadline := time.After(3 * time.Second)
	for srv.Endpoint() == "" || strings.HasSuffix(srv.Endpoint(), ":0") {
		select {
		case err := <-started:
			cancel()
			if err != nil {
				t.Fatalf("Start failed before binding: %v", err)
			}
			t.Fatal("Start returned nil while endpoint is still unresolved")
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
		<-started
	}
}

// TestHTTPServer_TLSRoundTrip 用真实 https 请求验证证书来自契约：客户端信任该
// CA 时 200，不信任时握手失败，且 Endpoint 的 scheme 是 https。
func TestHTTPServer_TLSRoundTrip(t *testing.T) {
	c := certs(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "pong")
	})

	srv := NewHTTPServer(&bootstrapv1.Server_Http{Addr: "127.0.0.1:0", Tls: c.ServerTLS()}, mux)
	stop := startTLS(t, srv)
	defer stop()

	ep := srv.Endpoint()
	if !strings.HasPrefix(ep, "https://") {
		t.Fatalf("scheme = %q, want https://（契约声明了 tls 段）", ep)
	}

	t.Run("trusted client", func(t *testing.T) {
		clientTLS, err := tlsclient(c)
		if err != nil {
			t.Fatalf("client tls: %v", err)
		}
		body := httpGet(t, &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}, Timeout: 3 * time.Second}, ep+"/ping")
		if body != "pong" {
			t.Fatalf("body = %q, want pong", body)
		}
	})

	t.Run("untrusted client", func(t *testing.T) {
		other, err := tlsclient(certs(t)) // 只信任另一套 CA
		if err != nil {
			t.Fatalf("client tls: %v", err)
		}
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: other}, Timeout: 3 * time.Second}
		resp, err := client.Get(ep + "/ping")
		if err == nil {
			_ = resp.Body.Close()
			t.Fatal("客户端接受了不在 ca 池里的服务端证书")
		}
		if !strings.Contains(err.Error(), "certificate") {
			t.Errorf("err = %v, want 证书校验失败", err)
		}
	})

	t.Run("plaintext rejected", func(t *testing.T) {
		// Go 的 TLS 服务端识别出「明文打到 HTTPS 端口」会回 400，不会把请求
		// 交给业务 handler——所以断言的是「拿不到 pong」，而不是「连接报错」。
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://" + strings.TrimPrefix(ep, "https://") + "/ping")
		if err != nil {
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusOK || strings.TrimSpace(string(b)) == "pong" {
			t.Fatalf("TLS 端口把明文请求交给了业务 handler: status=%d body=%q", resp.StatusCode, b)
		}
	})
}

// TestHTTPServer_TLSIncompleteContractFailsFast 契约写了 tls 段却配不出证书：
// Start 必须报错且不留下监听器（否则 Endpoint 会报告一个已关闭的地址，
// 服务注册到发现面即成幽灵节点）。
func TestHTTPServer_TLSIncompleteContractFailsFast(t *testing.T) {
	srv := NewHTTPServer(&bootstrapv1.Server_Http{Addr: "127.0.0.1:0", Tls: &bootstrapv1.Server_TLS{}}, http.NewServeMux())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := srv.Start(ctx)
	if err == nil {
		t.Fatal("空 tls 段应启动失败，却返回 nil")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Errorf("err = %v, want 指出缺证书", err)
	}
	if ep := srv.Endpoint(); ep != "" {
		t.Errorf("启动失败后 Endpoint = %q, want 空（不得报告未监听的地址）", ep)
	}
}

func certs(t *testing.T) *tlstest.Certs {
	t.Helper()
	c, err := tlstest.Generate()
	if err != nil {
		t.Fatalf("tlstest.Generate: %v", err)
	}
	return c
}

// tlsclient 用 tlsconf 把契约的客户端段翻译成 tls.Config——被测的证书语义
// 与实际消费方（transport）同源。
func tlsclient(c *tlstest.Certs) (*tls.Config, error) {
	return tlsconf.Client(c.ClientTLS())
}

func httpGet(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", resp.StatusCode, b)
	}
	return string(b)
}
