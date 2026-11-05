package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确、Apply 落声明时**携带工厂**；
//   2. 段在 → Build 成功：产物为 *mcp.Server；
//   3. Plan 声明的节点键/阶段与段路径同源（引擎按此调度）；
//   4. 子段缺失 → (nil, nil, nil) 跳过；address 必填 fail-fast；
//   5. 顶层 server 段缺失 → bug 级错误；
//   6. WithServerOptions / WithAfterBuild 经 AppProvider 透传生效。
//
// mcp.NewServer 仅构造（监听在 Start），离线可实例化；Build 产物不等 Start——
// endpoint 在 Start 前为空，构造判据以类型断言为准（HTTP 落点判据在 contract_test）。

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/appkit"
	mcp "github.com/kalandramo/bald/transport/mcp"
)

func mcpBootstrap(server *bootstrapv1.Server) *appkit.ApplyScope {
	return &appkit.ApplyScope{Config: &bootstrapv1.BootstrapConfig{Server: server}}
}

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "server.mcp" {
		t.Fatalf("Segments() = %v, want [server.mcp]", segs)
	}
}

// Apply 落声明的是携带工厂的接收者本身——零值副本会让 Build 直呼 nil 工厂。
func TestAppProvider_ApplyPreservesFactory(t *testing.T) {
	d := &appkit.Decl{}
	AppProvider().Apply(d)
	if len(d.Providers) != 1 {
		t.Fatalf("decl providers = %d, want 1", len(d.Providers))
	}
	srv, _, err := d.Providers[0].Build(context.Background(),
		*mcpBootstrap(&bootstrapv1.Server{Mcp: &bootstrapv1.Server_Mcp{Address: "127.0.0.1:19821"}}),
		appkit.Node{})
	if err != nil {
		t.Fatalf("Build via decl: %v", err)
	}
	if srv == nil {
		t.Fatal("srv = nil via decl provider, factory was lost in Apply")
	}
}

func TestAppProvider_PlanDeclaresSegmentNode(t *testing.T) {
	nodes, err := AppProvider().Plan(context.Background(), appkit.PlanScope{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("len(nodes) = %d, want 1", len(nodes))
	}
	n := nodes[0]
	if n.Key != "server.mcp" || n.Phase != appkit.PhaseServe {
		t.Fatalf("node = %+v, want Key=server.mcp Phase=PhaseServe", n)
	}
	if len(n.Segments) != 1 || n.Segments[0] != "server.mcp" {
		t.Fatalf("node segments = %v, want [server.mcp]", n.Segments)
	}
}

func TestAppProvider_BuildConstructsServer(t *testing.T) {
	srv, cleanup, err := AppProvider().Build(context.Background(),
		*mcpBootstrap(&bootstrapv1.Server{Mcp: &bootstrapv1.Server_Mcp{
			Address: "127.0.0.1:19822", Name: "built-by-provider", Version: "v1.2.3",
		}}),
		appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil (mcp factory yields no cleanup)")
	}
	if _, ok := srv.(*mcp.Server); !ok {
		t.Fatalf("srv type = %T, want *mcp.Server", srv)
	}
}

func TestAppProvider_AddressRequiredFailsFast(t *testing.T) {
	_, _, err := AppProvider().Build(context.Background(),
		*mcpBootstrap(&bootstrapv1.Server{Mcp: &bootstrapv1.Server_Mcp{}}),
		appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "server.mcp.address is required") {
		t.Fatalf("err = %v, want address required", err)
	}
}

func TestAppProvider_SubSectionMissingSkips(t *testing.T) {
	srv, cleanup, err := AppProvider().Build(context.Background(),
		*mcpBootstrap(&bootstrapv1.Server{}), appkit.Node{})
	if err != nil {
		t.Fatalf("Build error = %v, want nil (skip)", err)
	}
	if srv != nil || cleanup != nil {
		t.Fatalf("(srv, cleanup) = (%v, isNil=%v), want (nil, nil) on skip", srv, cleanup == nil)
	}
}

func TestAppProvider_TopSectionMissingIsBug(t *testing.T) {
	_, _, err := AppProvider().Build(context.Background(),
		appkit.ApplyScope{Config: &bootstrapv1.BootstrapConfig{}}, appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "report this as a bug") {
		t.Fatalf("err = %v, want bug report (engine gates by exact path)", err)
	}
}

// TestAppProvider_OptionsPassthrough 业务接线经 AppProvider 的变参透传。
func TestAppProvider_OptionsPassthrough(t *testing.T) {
	var hooked bool
	srv, _, err := AppProvider(
		WithServerOptions(mcp.WithServerName("passthrough")),
		WithAfterBuild(func(context.Context, *mcp.Server) error { hooked = true; return nil }),
	).Build(context.Background(),
		*mcpBootstrap(&bootstrapv1.Server{Mcp: &bootstrapv1.Server_Mcp{Address: "127.0.0.1:19823"}}),
		appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !hooked {
		t.Fatal("after-build hook not invoked via AppProvider (options not passed through)")
	}
	if srv == nil {
		t.Fatal("srv = nil")
	}
}
