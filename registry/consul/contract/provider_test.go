package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确、Option 嵌入面可用（Apply 追加进声明）；
//   2. 顶层段缺失 → Build 报 bug 级错误；
//   3. 子段缺失 → 复用 Provider 的 fail-fast。
//
// consul.New 只构造 client（不建连），离线可测；连通性由后端自身测试覆盖。

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/appkit"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "registry.consul" {
		t.Fatalf("Segments() = %v, want [registry.consul]", segs)
	}
}

func TestAppProvider_ApplyAddsToDecl(t *testing.T) {
	d := &appkit.Decl{}
	AppProvider().Apply(d)
	if len(d.Providers) != 1 {
		t.Fatalf("decl providers = %d, want 1", len(d.Providers))
	}
}

func TestAppProvider_PlanNode(t *testing.T) {
	nodes, err := AppProvider().Plan(context.Background(), appkit.PlanScope{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	n := nodes[0]
	if n.Key != "registry.consul" || n.Phase != appkit.PhaseBootstrap ||
		n.Priority != appkit.PriorityRegistry || n.Reload != appkit.ReloadRestart ||
		n.EffectName != "appkit:registrar-client" {
		t.Fatalf("node = %+v, want registry.consul bootstrap/registry/restart", n)
	}
}

func TestAppProvider_TopSectionMissingIsBug(t *testing.T) {
	scope := appkit.ApplyScope{Config: &bootstrapv1.BootstrapConfig{}}
	_, _, err := appProvider{}.Build(context.Background(), scope, appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "report this as a bug") {
		t.Fatalf("err = %v, want bug report (engine gates by exact path)", err)
	}
}

func TestAppProvider_SubSectionMissingFailsFast(t *testing.T) {
	scope := appkit.ApplyScope{Config: &bootstrapv1.BootstrapConfig{
		Registry: &bootstrapv1.Registry{},
	}}
	_, _, err := appProvider{}.Build(context.Background(), scope, appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "consul section is missing") {
		t.Fatalf("err = %v, want consul section missing", err)
	}
}
