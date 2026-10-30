package appkit

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
)

// 约定装配：阶段 B 构建 → Cache 取用 → 停机 cleanup。

func TestAssemble_ContractCacheLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	// stub 实例（真实 Cache 语义由各后端包测试覆盖；本测试验证 appkit
	// 生命周期：构建→取用→停机 cleanup）。
	type stubCache struct{ name string }
	cleaned := false
	prov := stubDomainProvider("cache", "local",
		func(c *bootstrapv1.BootstrapConfig) bool { return c.GetCache().GetLocal() != nil },
		&stubCache{name: "local"}, func() { cleaned = true })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Cache = &bootstrapv1.Cache{
		Local: &bootstrapv1.Cache_Local{Size: 1024 * 1024, DefaultTtlSeconds: 60},
	}

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)), prov)
	// 构造期（阶段 A）尚未装载契约：Cache 为空
	if _, ok := a.lookup("cache.local"); ok {
		t.Fatal("Cache should be empty before Run (phase B not reached)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	cli, ok := a.lookup("cache.local")
	if !ok {
		t.Fatal("cache instance not built from contract")
	}
	if c, ok := cli.(*stubCache); !ok || c.name != "local" {
		t.Fatalf("instance type mismatch: %T", cli)
	}
	if all := resourceMap(t, a, "cache"); len(all) != 1 {
		t.Fatalf("Resource(\"cache\") snapshot = %v, want 1 entry", all)
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

// 契约 cache 段存在但无 cache Provider → 阶段 B fail-fast。
func TestAssemble_CacheSectionWithoutTable(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Cache = &bootstrapv1.Cache{
		Local: &bootstrapv1.Cache_Local{Size: 1024},
	}

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)))
	err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no consumer") || !strings.Contains(err.Error(), "cache") {
		t.Fatalf("expected no-consumer error for cache, got %v", err)
	}
}

// cache 段缺失 → no-op，不要求 Provider。
func TestAssemble_NoCacheSection(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := a.lookup("cache.local"); ok {
		t.Fatal("no cache should be built without cache section")
	}
}
