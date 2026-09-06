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

	time.Sleep(100 * time.Millisecond)

	if srv.listener == nil {
		t.Fatal("expected listener to be set after Start")
	}

	addr := srv.listener.Addr().String()

	resp, err := http.Get("http://" + addr + "/health")
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
	var headerSeen string

	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			headerSeen = r.Header.Get("X-Test")
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
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	req, _ := http.NewRequest("GET", "http://"+addr+"/test", nil)
	req.Header.Set("X-Test", "middleware-works")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

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
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	resp, err := http.Get("http://" + addr + "/check")
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
