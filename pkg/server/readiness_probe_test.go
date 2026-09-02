package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	confv1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/config/v1"
)

// TestReady_AllNil 聚合所有依赖通过时返回 nil。
func TestReady_AllNil(t *testing.T) {
	fn := Ready(nil, func(context.Context) error { return nil })
	if err := fn(context.Background()); err != nil {
		t.Fatalf("Ready with all-pass deps should return nil, got %v", err)
	}
}

// TestReady_AggregatesFailures 聚合保留全部失败原因（errors.Is 命中每个错误）。
func TestReady_AggregatesFailures(t *testing.T) {
	errDB := errors.New("db down")
	errCache := errors.New("cache down")
	fn := Ready(
		func(context.Context) error { return errDB },
		func(context.Context) error { return errCache },
		func(context.Context) error { return nil },
	)
	err := fn(context.Background())
	if err == nil {
		t.Fatal("Ready should return error when any dep fails")
	}
	if !errors.Is(err, errDB) || !errors.Is(err, errCache) {
		t.Fatalf("aggregated error must wrap both deps, got %v", err)
	}
}

// TestHTTPServer_CustomProbePaths WithProbePaths 覆盖探针路径：
// 自定义路径生效（存活 200 / 就绪 503），默认路径不再注册（404）。
func TestHTTPServer_CustomProbePaths(t *testing.T) {
	srv := NewHTTPServer(
		&confv1.Http{Addr: ":0"},
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

// TestGRPCServer_WithPollInterval WithReadinessPollInterval 覆盖轮询间隔。
func TestGRPCServer_WithPollInterval(t *testing.T) {
	g := NewGRPCServerWithRegister(
		&confv1.Grpc{Addr: ":0"},
		nil, nil, nil,
		WithReadinessPollInterval(500*time.Millisecond),
	)
	if g.readinessInterval != 500*time.Millisecond {
		t.Fatalf("readiness interval = %v, want 500ms", g.readinessInterval)
	}
	// 默认值。
	g2 := NewGRPCServerWithRegister(&confv1.Grpc{Addr: ":0"}, nil, nil, nil)
	if g2.readinessInterval != defaultReadinessPollInterval {
		t.Fatalf("default readiness interval = %v, want %v", g2.readinessInterval, defaultReadinessPollInterval)
	}
}
