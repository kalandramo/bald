package appkit

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
)

// --- CacheRegistry：显式注册与 fail-fast ---

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

// --- FromBootstrap 契约装配：阶段 B 构建 → Cache 取用 → 停机 cleanup ---

func TestFromBootstrap_ContractCacheLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	// stub 实例（真实 Cache 语义由 cache/<backend>/contract 测试覆盖；
	// 本测试验证 appkit 生命周期：构建→取用→停机 cleanup）。
	type stubCache struct{ name string }
	cleaned := false
	cr := NewCacheRegistry()
	cr.MustRegister("local", func(context.Context, *bootstrapv1.Cache) (any, func(), error) {
		return &stubCache{name: "local"}, func() { cleaned = true }, nil
	})

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Cache = &bootstrapv1.Cache{
		Local: &bootstrapv1.Cache_Local{Size: 1024 * 1024, DefaultTtlSeconds: 60},
	}

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)), WithCacheRegistry(cr))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	// 构造期（阶段 A）尚未装载契约：Cache 为空
	if _, ok := a.Cache("local"); ok {
		t.Fatal("Cache should be empty before Run (phase B not reached)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	cli, ok := a.Cache("local")
	if !ok {
		t.Fatal("cache instance not built from contract")
	}
	if c, ok := cli.(*stubCache); !ok || c.name != "local" {
		t.Fatalf("instance type mismatch: %T", cli)
	}
	if all := a.Caches(); len(all) != 1 {
		t.Fatalf("Caches() = %v, want 1 entry", all)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s")
	}
	if !cleaned {
		t.Fatal("cache cleanup should run on shutdown")
	}
}

// 契约 cache 段存在但未接 CacheRegistry → 阶段 B fail-fast。
func TestFromBootstrap_CacheSectionWithoutTable(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Cache = &bootstrapv1.Cache{
		Local: &bootstrapv1.Cache_Local{Size: 1024},
	}

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)))
	if err != nil {
		t.Fatalf("FromBootstrap (构造期不校验合并后配置): %v", err)
	}
	err = a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "CacheRegistry") {
		t.Fatalf("expected CacheRegistry missing error, got %v", err)
	}
}

// cache 段缺失 → no-op，不要求接注册表。
func TestFromBootstrap_NoCacheSection(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := a.Cache("local"); ok {
		t.Fatal("no cache should be built without cache section")
	}
}
