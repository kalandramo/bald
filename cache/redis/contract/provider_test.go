package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确、Option 嵌入面可用（Apply 追加进声明）；
//   2. 段在 → Build 成功：产物落 map[string]any{"redis": ...}，读写往返，
//      cleanup 关闭连接池；
//   3. 顶层段缺失 → Build 报 bug 级错误（引擎按精确路径 gate，正常不触达）；
//   4. 子段缺失 → 复用 Provider 的 fail-fast（不产半成品实例）。
//
// 接真实内存 Redis（miniredis），不用 fake client——与 contract_test 同原则。

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/cache"
	"github.com/kalandramo/bald/pkg/appkit"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "cache.redis" {
		t.Fatalf("Segments() = %v, want [cache.redis]", segs)
	}
}

func TestAppProvider_ApplyAddsToDecl(t *testing.T) {
	d := &appkit.Decl{}
	AppProvider().Apply(d)
	if len(d.Providers) != 1 {
		t.Fatalf("decl providers = %d, want 1", len(d.Providers))
	}
}

func TestAppProvider_BuildRoundTripAndCleanup(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	scope := appkit.BuildScope{Config: &bootstrapv1.BootstrapConfig{
		Cache: &bootstrapv1.Cache{
			Redis: &bootstrapv1.Cache_Redis{Addr: mr.Addr(), KeyPrefix: "app:"},
		},
	}}
	product, cleanup, err := appProvider{}.Build(context.Background(), scope)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup = nil, want non-nil")
	}
	m, ok := product.(map[string]any)
	if !ok {
		t.Fatalf("product type = %T, want map[string]any", product)
	}
	cli, ok := m["redis"].(cache.Cache)
	if !ok {
		t.Fatalf("product[redis] type = %T, want cache.Cache", m["redis"])
	}

	ctx := context.Background()
	if err := cli.Set(ctx, "k", []byte("v"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !mr.Exists("app:k") {
		t.Fatal("key prefix not applied")
	}

	if err := cleanup(ctx); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := cli.Get(ctx, "k"); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("after cleanup Get err = %v, want closed", err)
	}
}

func TestAppProvider_TopSectionMissingIsBug(t *testing.T) {
	scope := appkit.BuildScope{Config: &bootstrapv1.BootstrapConfig{}}
	_, _, err := appProvider{}.Build(context.Background(), scope)
	if err == nil || !strings.Contains(err.Error(), "report this as a bug") {
		t.Fatalf("err = %v, want bug report (engine gates by exact path)", err)
	}
}

func TestAppProvider_SubSectionMissingFailsFast(t *testing.T) {
	scope := appkit.BuildScope{Config: &bootstrapv1.BootstrapConfig{
		Cache: &bootstrapv1.Cache{},
	}}
	_, _, err := appProvider{}.Build(context.Background(), scope)
	if err == nil || !strings.Contains(err.Error(), "redis section is missing") {
		t.Fatalf("err = %v, want redis section missing", err)
	}
}
