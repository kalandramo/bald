package httpserver

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// startTestServer 在随机端口启动 HTTPServer 并返回 base URL（Start 在后台
// goroutine；ctx 取消时收口）。
func startTestServer(t *testing.T, cfg *bootstrapv1.Server_Http, handler http.Handler) string {
	t.Helper()
	srv := NewHTTPServer(cfg, handler)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Start(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	base := ""
	for time.Now().Before(deadline) {
		if srv.ln != nil {
			base = "http://" + srv.ln.Addr().String()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if base == "" {
		t.Fatal("server did not start within 5s")
	}
	return base
}

// TestMaxBodyBytes_EnforcedAndContractWiring 钉 AR-16 契约字段 max_body_bytes：
// 构造点把入站请求包 MaxBytesReader——超限返回 413 且不再读取剩余字节
// （防大 payload 打满内存）。两驱动（net/http 直驱与 gin.Engine 注入）经同一
// 构造点 NewHTTPServer，包装对两者同样生效。
func TestMaxBodyBytes_EnforcedAndContractWiring(t *testing.T) {
	// handler 形态对齐真实业务（bind 层读 body 失败即回 413）：MaxBytesReader
	// 的语义是「让读取报错」——框架层无法代替 handler 决定状态码，但必须保证
	// 超限时读取收到错误且连接不再吞字节。
	failOnRead := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	for _, tc := range []struct {
		name    string
		handler http.Handler
	}{
		{"net_http_handler", failOnRead},
		// gin 驱动的 handler 是 gin.Engine（实现 http.Handler）——同一构造点
		// 包装对两驱动同样生效，这里用等价 handler 代表。
		{"engine_like_handler", failOnRead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &bootstrapv1.Server_Http{
				Addr:         ":0",
				MaxBodyBytes: 10,
			}
			base := startTestServer(t, cfg, tc.handler)

			// 20 字节 body 超限（上限 10）→ 413。
			req, _ := http.NewRequest(http.MethodPost, base+"/x", strings.NewReader("0123456789ABCDEFGHIJ"))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413（超限请求体应被拒绝）", resp.StatusCode)
			}
		})
	}
}

// TestMaxBodyBytes_ZeroMeansUnlimited 对照：未配置（0）时不设上限——既有行为
// 保持，大 body 正常通过。
func TestMaxBodyBytes_ZeroMeansUnlimited(t *testing.T) {
	cfg := &bootstrapv1.Server_Http{Addr: ":0"}
	base := startTestServer(t, cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
	}))

	big := strings.Repeat("x", 4096)
	req, _ := http.NewRequest(http.MethodPost, base+"/x", strings.NewReader(big))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200（未配置 max_body_bytes 时不设上限）", resp.StatusCode)
	}
}
