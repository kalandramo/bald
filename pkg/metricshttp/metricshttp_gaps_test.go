package metricshttp

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	log "github.com/kalandramo/bald/log"
)

// errCapture 记录 Error 调用并经 channel 同步，用于确定性地触达 Start 的异步
// 监听失败日志分支（不依赖 sleep 猜协程调度）。
type errCapture struct {
	errCh chan string
}

func (errCapture) Debug(context.Context, string, ...any) {}
func (errCapture) Info(context.Context, string, ...any)  {}
func (errCapture) Warn(context.Context, string, ...any)  {}
func (e errCapture) Error(_ context.Context, msg string, _ ...any) {
	select {
	case e.errCh <- msg:
	default:
	}
}
func (errCapture) Enabled(log.Level) bool   { return true }
func (e errCapture) With(...any) log.Logger { return e }

// TestStart_DefaultAddr 空 addr 走 DefaultAddr（:9091）。
func TestStart_DefaultAddr(t *testing.T) {
	lis, err := net.Listen("tcp", DefaultAddr)
	if err != nil {
		t.Skipf("default addr %s occupied: %v", DefaultAddr, err)
	}
	_ = lis.Close()

	srv := Start("", "/m", http.NotFoundHandler())
	// 等监听就绪再 Stop，避免 Shutdown 先于 ListenAndServe 绑定的竞态。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, derr := net.DialTimeout("tcp", "127.0.0.1:9091", 100*time.Millisecond)
		if derr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestStart_DefaultPath 空 path 走 DefaultPath（/metrics）。
func TestStart_DefaultPath(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	srv := Start(addr, "", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer func() { _ = srv.Stop(context.Background()) }()

	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for {
		resp, err = http.Get("http://" + addr + DefaultPath)
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
		t.Fatalf("default path /metrics status = %d", resp.StatusCode)
	}
}

// TestStart_ListenError 端口被占 → ListenAndServe 失败 → 异步 log.Error（旁路不 panic）。
func TestStart_ListenError(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer func() { _ = lis.Close() }()
	addr := lis.Addr().String() // 占住不放，Start 同地址必然 bind 失败

	cap := errCapture{errCh: make(chan string, 1)}
	orig := log.GetLogger()
	log.SetLogger(cap)
	t.Cleanup(func() { log.SetLogger(orig) })

	srv := Start(addr, "/metrics", http.NotFoundHandler())
	defer func() { _ = srv.Stop(context.Background()) }()

	select {
	case msg := <-cap.errCh:
		if msg != "metrics server stopped" {
			t.Errorf("unexpected log message: %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected async log.Error on listen failure")
	}
}
