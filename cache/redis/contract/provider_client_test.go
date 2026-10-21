package contract

// provider_client_test.go —— 端到端钉子：业务经
// AppProvider 产物（map[string]any{"redis": *rediscache.Cache}）取原生
// go-redis client，且拿到的是 Provider 自建、cleanup 负责关闭的**同一实例**
// ——cleanup 之后经同一句柄操作即报 closed。

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	rediscache "github.com/kalandramo/bald/cache/redis"
	"github.com/kalandramo/bald/pkg/appkit"
)

func TestAppProvider_ProductExposesNativeClient(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	scope := appkit.BuildScope{Config: &bootstrapv1.BootstrapConfig{
		Cache: &bootstrapv1.Cache{
			Redis: &bootstrapv1.Cache_Redis{Addr: mr.Addr()},
		},
	}}
	product, cleanup, err := appProvider{}.Build(context.Background(), scope)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	m, ok := product.(map[string]any)
	if !ok {
		t.Fatalf("product type = %T, want map[string]any", product)
	}
	rc, ok := m["redis"].(*rediscache.Cache)
	if !ok {
		t.Fatalf("product[redis] type = %T, want *rediscache.Cache", m["redis"])
	}

	ctx := context.Background()
	rdb := rc.Client()
	if rdb == nil {
		t.Fatal("Client() = nil, want the provider-built native client")
	}
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("Ping via native client: %v", err)
	}

	if err := cleanup(ctx); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if err := rdb.Ping(ctx).Err(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("after cleanup Ping err = %v, want closed (same instance the cleanup owns)", err)
	}
}
