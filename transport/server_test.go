package transport

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// stubServer 最小 Server 实现，用于契约层测试（不依赖任何具体协议实现）。
type stubServer struct {
	started chan struct{}
	stopped chan struct{}
}

func newStubServer() *stubServer {
	return &stubServer{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (s *stubServer) Start(context.Context) error { close(s.started); return nil }
func (s *stubServer) Stop(context.Context) error  { close(s.stopped); return nil }
func (s *stubServer) Endpoint() string            { return "stub://127.0.0.1:8080" }

// TestServe_Lifecycle 验证 Serve 的独立生命周期：ctx 取消 → Stop（带超时 ctx）。
func TestServe_Lifecycle(t *testing.T) {
	srv := newStubServer()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv, 2*time.Second) }()

	// Serve 内部 goroutine Start 是异步的，等它执行完再取消。
	select {
	case <-srv.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not start the server in time")
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after ctx cancel")
	}

	select {
	case <-srv.stopped:
	default:
		t.Fatal("Serve should call Stop after ctx cancel")
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

// TestReady_AllNil 聚合所有依赖通过时返回 nil；nil 成员被跳过。
func TestReady_AllNil(t *testing.T) {
	fn := Ready(nil, func(context.Context) error { return nil })
	if err := fn(context.Background()); err != nil {
		t.Fatalf("Ready with all-pass deps should return nil, got %v", err)
	}
}

// TestExtract_ExplicitIPKept：显式指定 IP 时 Extract 应原样保留，不被覆盖。
func TestExtract_ExplicitIPKept(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	got, err := Extract("10.0.0.5:8080", ln)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got != "10.0.0.5:8080" {
		t.Fatalf("Extract = %q, want 10.0.0.5:8080 (explicit IP kept)", got)
	}
}
