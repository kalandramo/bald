package redis

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// TestCache_Get_MissThenHit 用真实 miniredis 验证 Cache-Aside：首次未命中加载，
// 回填后二次读取命中缓存（loader 不再被调用）。
func TestCache_Get_MissThenHit(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	c, err := New(mr.Addr())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	calls := 0
	loader := func(context.Context) (string, error) {
		calls++
		return "v1", nil
	}

	// 第一次：未命中，经 loader 加载。
	got, err := c.Get(ctx, SecretKey("t-default", "s-1"), loader)
	if err != nil {
		t.Fatalf("Get#1: %v", err)
	}
	if got != "v1" || calls != 1 {
		t.Fatalf("Get#1: got=%q calls=%d want v1/1", got, calls)
	}
	// 第二次：命中缓存，loader 不再调用。
	got, err = c.Get(ctx, SecretKey("t-default", "s-1"), loader)
	if err != nil {
		t.Fatalf("Get#2: %v", err)
	}
	if got != "v1" || calls != 1 {
		t.Fatalf("Get#2 should hit cache: got=%q calls=%d want v1/1", got, calls)
	}
}

// TestCache_DisabledDirectLoader 无地址时退化为直连 loader（不连 Redis）。
func TestCache_DisabledDirectLoader(t *testing.T) {
	c, err := New("")
	if err != nil {
		t.Fatalf("New(\"\"): %v", err)
	}
	if c.rdb != nil {
		t.Fatalf("disabled cache should have nil rdb")
	}
	got, err := c.Get(context.Background(), "k", func(context.Context) (string, error) { return "x", nil })
	if err != nil || got != "x" {
		t.Fatalf("disabled Get: got=%q err=%v", got, err)
	}
}

// TestCache_TenantIsolationKey 缓存键含租户，避免跨租户泄漏。
func TestCache_TenantIsolationKey(t *testing.T) {
	if SecretKey("t-a", "s1") == SecretKey("t-b", "s1") {
		t.Fatalf("tenant must be part of key")
	}
}
