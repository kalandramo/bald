package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确、Option 嵌入面可用（Apply 追加进声明）；
//   2. 段在 → Build 成功：产物为 registry.Registrar，cleanup 关闭 client；
//   3. 顶层段缺失 → Build 报 bug 级错误；
//   4. 子段缺失 / endpoints 必填 → 复用 Provider 的 fail-fast。
//
// clientv3 构造为懒拨号（无 WithBlock），合法格式 endpoint 离线可构造；
// 连通性由既有 registry_test 覆盖，此处不触真集群。

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/appkit"
	registry "github.com/kalandramo/bald/registry"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "registry.etcd" {
		t.Fatalf("Segments() = %v, want [registry.etcd]", segs)
	}
}

func TestAppProvider_ApplyAddsToDecl(t *testing.T) {
	d := &appkit.Decl{}
	AppProvider().Apply(d)
	if len(d.Providers) != 1 {
		t.Fatalf("decl providers = %d, want 1", len(d.Providers))
	}
}

func TestAppProvider_BuildConstructsRegistrar(t *testing.T) {
	scope := appkit.BuildScope{Config: &bootstrapv1.BootstrapConfig{
		Registry: &bootstrapv1.Registry{
			Etcd: &bootstrapv1.Registry_Etcd{
				Endpoints: []string{"127.0.0.1:2379"},
				Prefix:    "bald-test",
			},
		},
	}}
	product, cleanup, err := appProvider{}.Build(context.Background(), scope)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup = nil, want non-nil (client lifecycle)")
	}
	r, ok := product.(registry.Registrar)
	if !ok || r == nil {
		t.Fatalf("product = (%T, %v), want non-nil registry.Registrar", product, product)
	}

	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	// Close 幂等：重复调用安全（停机 Effect 与测试 cleanup 双路径）。
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup (second): %v", err)
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
		Registry: &bootstrapv1.Registry{},
	}}
	_, _, err := appProvider{}.Build(context.Background(), scope)
	if err == nil || !strings.Contains(err.Error(), "etcd section is missing") {
		t.Fatalf("err = %v, want etcd section missing", err)
	}
}

func TestAppProvider_EndpointsRequiredFailsFast(t *testing.T) {
	scope := appkit.BuildScope{Config: &bootstrapv1.BootstrapConfig{
		Registry: &bootstrapv1.Registry{Etcd: &bootstrapv1.Registry_Etcd{}},
	}}
	_, _, err := appProvider{}.Build(context.Background(), scope)
	if err == nil || !strings.Contains(err.Error(), "endpoints is required") {
		t.Fatalf("err = %v, want endpoints required", err)
	}
}
