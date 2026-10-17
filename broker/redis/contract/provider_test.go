package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确、Option 嵌入面可用（Apply 追加进声明）；
//   2. 段在 → Build 成功：产物落 map[string]any{"redis": ...} 且实现
//      broker.Broker，cleanup 断开连接；
//   3. 顶层段缺失 → Build 报 bug 级错误（引擎按精确路径 gate，正常不触达）；
//   4. 子段缺失 → 复用 Provider 的 fail-fast。
//
// 接真实内存 Redis（miniredis），不用 fake client——与 contract_test 同原则。

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/pkg/appkit"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "broker.redis" {
		t.Fatalf("Segments() = %v, want [broker.redis]", segs)
	}
}

func TestAppProvider_ApplyAddsToDecl(t *testing.T) {
	d := &appkit.Decl{}
	AppProvider().Apply(d)
	if len(d.Providers) != 1 {
		t.Fatalf("decl providers = %d, want 1", len(d.Providers))
	}
}

func TestAppProvider_BuildConnectsAndCleanup(t *testing.T) {
	mr := miniredis.RunT(t)

	scope := appkit.BuildScope{Config: &bootstrapv1.BootstrapConfig{
		Broker: &bootstrapv1.Broker{
			Redis: &bootstrapv1.Broker_Redis{Address: mr.Addr()},
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
	if _, ok := m["redis"].(broker.Broker); !ok {
		t.Fatalf("product[redis] type = %T, want broker.Broker", m["redis"])
	}

	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
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
		Broker: &bootstrapv1.Broker{},
	}}
	_, _, err := appProvider{}.Build(context.Background(), scope)
	if err == nil || !strings.Contains(err.Error(), "redis section is missing") {
		t.Fatalf("err = %v, want redis section missing", err)
	}
}
