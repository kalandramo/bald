package graphql

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// NewServer + basic configuration
// ---------------------------------------------------------------------------

func TestNewServer_Defaults(t *testing.T) {
	srv := NewServer(":0")
	if srv.Addr() != ":0" {
		t.Errorf("Addr() = %q, want %q", srv.Addr(), ":0")
	}
	if srv.tlsConfig != nil {
		t.Error("expected nil tlsConfig by default")
	}
}

// ---------------------------------------------------------------------------
// Endpoint
// ---------------------------------------------------------------------------

func TestServer_Endpoint_HTTP(t *testing.T) {
	srv := NewServer(":0")
	ep := srv.Endpoint()
	if !strings.HasPrefix(ep, "http://") {
		t.Errorf("expected http:// prefix, got %q", ep)
	}
}

func TestServer_Endpoint_HTTPS(t *testing.T) {
	srv := NewServer(":0", WithTLSConfig(&tls.Config{}))
	ep := srv.Endpoint()
	if !strings.HasPrefix(ep, "https://") {
		t.Errorf("expected https:// prefix, got %q", ep)
	}
}

func TestServer_Endpoint_NormalizeWildcard(t *testing.T) {
	srv := NewServer("0.0.0.0:8080")
	ep := srv.Endpoint()
	if !strings.Contains(ep, "localhost") {
		t.Errorf("expected localhost in endpoint, got %q", ep)
	}
}

// ---------------------------------------------------------------------------
// Start + Stop lifecycle
// ---------------------------------------------------------------------------

// waitEndpoint 轮询 Endpoint() 直到 Start() 把监听地址发布出来。
// 配 ":0" 时 Start 之前 Endpoint() 以 ":0" 结尾，拿到真实端口即就绪。
// 取代旧的「固定 sleep + 直接读 s.listener」：后者与 Start 的写入构成数据竞争。
func waitEndpoint(t *testing.T, s *Server) string {
	t.Helper()
	for i := 0; i < 500; i++ {
		if ep := s.Endpoint(); !strings.HasSuffix(ep, ":0") {
			return ep
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Start() did not publish a listener within 5s")
	return ""
}

func TestServer_StartStop(t *testing.T) {
	srv := NewServer("127.0.0.1:0")

	srv.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	ep := waitEndpoint(t, srv)

	resp, err := http.Get(ep + "/health")
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Start returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop within 5s")
	}
}

// ---------------------------------------------------------------------------
// Middleware via WithMiddleware
// ---------------------------------------------------------------------------

func TestServer_Middleware(t *testing.T) {
	// 中间件在服务端 goroutine 里观察请求头，须经 channel 交给测试
	// goroutine；裸共享变量的读写本身就是一次数据竞争。
	headerCh := make(chan string, 1)

	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case headerCh <- r.Header.Get("X-Test"):
			default:
			}
			next.ServeHTTP(w, r)
		})
	}

	srv := NewServer("127.0.0.1:0", WithMiddleware(mw))
	srv.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)

	ep := waitEndpoint(t, srv)
	req, _ := http.NewRequest("GET", ep+"/test", nil)
	req.Header.Set("X-Test", "middleware-works")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var headerSeen string
	select {
	case headerSeen = <-headerCh:
	case <-time.After(5 * time.Second):
		t.Fatal("middleware was never invoked")
	}

	if headerSeen != "middleware-works" {
		t.Errorf("middleware did not see header, got %q", headerSeen)
	}

	cancel()
	time.Sleep(200 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// Middleware via Use
// ---------------------------------------------------------------------------

func TestServer_Use(t *testing.T) {
	srv := NewServer("127.0.0.1:0")
	srv.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-From-Middleware", "yes")
			next.ServeHTTP(w, r)
		})
	})
	srv.HandleFunc("/check", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)

	ep := waitEndpoint(t, srv)
	resp, err := http.Get(ep + "/check")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("X-From-Middleware") != "yes" {
		t.Error("Use() middleware was not applied")
	}

	cancel()
	time.Sleep(200 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// Stop before Start (should not panic)
// ---------------------------------------------------------------------------

func TestServer_StopBeforeStart(t *testing.T) {
	srv := NewServer(":0")
	if err := srv.Stop(context.Background()); err != nil {
		t.Errorf("Stop before Start returned error: %v", err)
	}
}
