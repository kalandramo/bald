package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// --- CacheRegistry：显式注册与 fail-fast（自 pkg/appkit 迁入 2026-09-15）---

func TestCacheRegistry_FailFast(t *testing.T) {
	cr := NewCacheRegistry()
	cr.MustRegister("local", func(context.Context, *bootstrapv1.Cache) (any, func(), error) {
		return nil, nil, nil
	})

	// 重名
	err := cr.Register("local", func(context.Context, *bootstrapv1.Cache) (any, func(), error) {
		return nil, nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	// 空类型
	if err := cr.Register("", nil); err == nil {
		t.Fatal("expected empty type error")
	}
	// nil provider
	if err := cr.Register("redis", nil); err == nil {
		t.Fatal("expected nil provider error")
	}
	// Build：契约段整体为 nil → no-op
	clients, cleanup, err := cr.Build(context.Background(), nil)
	if err != nil || clients != nil || cleanup != nil {
		t.Fatalf("nil section should be no-op, got (clients=%v, cleanup=%t, err=%v)", clients, cleanup != nil, err)
	}
	// Build：零段非 nil 契约 → no-op
	clients, cleanup, err = cr.Build(context.Background(), &bootstrapv1.Cache{})
	if err != nil || clients == nil || len(clients) != 0 || cleanup != nil {
		t.Fatalf("empty section should be no-op, got (clients=%v, cleanup=%t, err=%v)", clients, cleanup != nil, err)
	}
	// Build：段存在但 provider 未注册（用未注册的 redis 段）
	_, _, err = cr.Build(context.Background(), &bootstrapv1.Cache{
		Redis: &bootstrapv1.Cache_Redis{Addr: "127.0.0.1:6379"},
	})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected not-registered error, got %v", err)
	}
}

// Build：段存在 → 查表构建，多段并存，全部返回，cleanup 逆序回放。
func TestCacheRegistry_BuildMultiSection(t *testing.T) {
	cr := NewCacheRegistry()
	cleaned := []string{}
	cr.MustRegister("local", func(context.Context, *bootstrapv1.Cache) (any, func(), error) {
		return "local-cache", func() { cleaned = append(cleaned, "local") }, nil
	})
	cr.MustRegister("redis", func(context.Context, *bootstrapv1.Cache) (any, func(), error) {
		return "redis-cache", func() { cleaned = append(cleaned, "redis") }, nil
	})

	clients, cleanup, err := cr.Build(context.Background(), &bootstrapv1.Cache{
		Local: &bootstrapv1.Cache_Local{Size: 1024},
		Redis: &bootstrapv1.Cache_Redis{Addr: "stub"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if clients["local"] != "local-cache" || clients["redis"] != "redis-cache" {
		t.Fatalf("clients mismatch: %v", clients)
	}
	cleanup()
	// 聚合 cleanup 逆序回放：redis（后建）先于 local
	if len(cleaned) != 2 || cleaned[0] != "redis" || cleaned[1] != "local" {
		t.Fatalf("cleanup order = %v, want [redis local]", cleaned)
	}
}

// Build：第二段构建失败 → 回滚第一段已建实例的 cleanup。
func TestCacheRegistry_BuildRollback(t *testing.T) {
	cr := NewCacheRegistry()
	rolled := false
	cr.MustRegister("local", func(context.Context, *bootstrapv1.Cache) (any, func(), error) {
		return "local-cache", func() { rolled = true }, nil
	})
	cr.MustRegister("redis", func(context.Context, *bootstrapv1.Cache) (any, func(), error) {
		return nil, nil, errors.New("boom")
	})

	_, _, err := cr.Build(context.Background(), &bootstrapv1.Cache{
		Local: &bootstrapv1.Cache_Local{Size: 1024},
		Redis: &bootstrapv1.Cache_Redis{Addr: "stub"},
	})
	if err == nil || !strings.Contains(err.Error(), "build cache redis") {
		t.Fatalf("expected redis build error, got %v", err)
	}
	if !rolled {
		t.Fatal("local cleanup should be rolled back on redis failure")
	}
}
