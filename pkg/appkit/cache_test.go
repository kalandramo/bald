package appkit

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
	"github.com/kalandramo/bald/log"
)

// FromBootstrap 契约装配：阶段 B 构建 → Cache 取用 → 停机 cleanup。
// Registry 行为测试（注册/段枚举/回滚）已随六域 Registry 迁 bootstrap
// （bootstrap/cache_test.go，2026-09-15）。

func TestFromBootstrap_ContractCacheLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	// stub 实例（真实 Cache 语义由 cache/<backend>/contract 测试覆盖；
	// 本测试验证 appkit 生命周期：构建→取用→停机 cleanup）。
	type stubCache struct{ name string }
	cleaned := false
	cr := baldbootstrap.NewCacheRegistry()
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
