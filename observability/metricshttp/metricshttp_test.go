package metricshttp

// metricshttp_test.go 覆盖暴露端监听面契约：
// 监听面契约（Start 起独立端口、自定义路径生效、缺省值钉住）。

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestStart(t *testing.T) {
	// 占随机端口（关闭后复用，竞态窗口可忽略）。
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("metrics-stub"))
	})
	srv := Start(addr, "/custom-metrics", handler)
	defer func() { _ = srv.Stop(context.Background()) }()

	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for {
		resp, err = http.Get("http://" + addr + "/custom-metrics")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server not ready: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// 缺省值：空 addr/path 走 :9091 / /metrics（仅断言常量，不起真端口避冲突）。
	if DefaultAddr != ":9091" || DefaultPath != "/metrics" {
		t.Fatalf("defaults changed: %q %q", DefaultAddr, DefaultPath)
	}
}

// TestStop 停机契约：Stop 后监听关闭（连接被拒），幂等安全（Shutdown 语义）。
func TestStop(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	srv := Start(addr, "/metrics", http.NotFoundHandler())
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// 二次 Stop 幂等（Shutdown 对已关闭 server 返回 nil）。
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, derr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if derr != nil {
			return // 监听已关
		}
		_ = c.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("listener still accepting after Stop")
}
