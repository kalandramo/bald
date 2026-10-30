package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确、Option 嵌入面可用（Apply 追加进声明）；
//   2. 顶层段缺失 → Build 报 bug 级错误；
//   3. 子段缺失 → 复用 Provider 的 fail-fast。
//
// kubernetes.New 走 rest.InClusterConfig，构建成功路径只能在集群内验证
// （见包注释）；此处只钉住装配面与失败面。

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/appkit"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "registry.kubernetes" {
		t.Fatalf("Segments() = %v, want [registry.kubernetes]", segs)
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
	if n.Key != "registry.kubernetes" || n.Phase != appkit.PhaseBootstrap ||
		n.Priority != appkit.PriorityRegistry || n.Reload != appkit.ReloadRestart ||
		n.EffectName != "appkit:registrar-client" {
		t.Fatalf("node = %+v, want registry.kubernetes bootstrap/registry/restart", n)
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
	if err == nil || !strings.Contains(err.Error(), "kubernetes section is missing") {
		t.Fatalf("err = %v, want kubernetes section missing", err)
	}
}
