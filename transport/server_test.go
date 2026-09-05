package transport

import (
	"context"
	"errors"
	"net"
	"testing"
)

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
