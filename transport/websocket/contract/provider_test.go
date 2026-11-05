package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确、Apply 落声明时**携带工厂**；
//   2. 段在 → Build 成功：产物为 *websocket.Server，端点即契约地址；
//   3. Plan 声明的节点键/阶段与段路径同源（引擎按此调度）；
//   4. 子段缺失 → (nil, nil, nil) 跳过；addr 必填 fail-fast；
//   5. 顶层 server 段缺失 → bug 级错误；
//   6. WithServerOptions / WithAfterBuild 经 AppProvider 透传生效。
//
// websocket.NewServer 仅构造（监听在 Start），离线可实例化。

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/appkit"
	ws "github.com/kalandramo/bald/transport/websocket"
)

func wsBootstrap(server *bootstrapv1.Server) *appkit.ApplyScope {
	return &appkit.ApplyScope{Config: &bootstrapv1.BootstrapConfig{Server: server}}
}

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "server.websocket" {
		t.Fatalf("Segments() = %v, want [server.websocket]", segs)
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
		*wsBootstrap(&bootstrapv1.Server{Websocket: &bootstrapv1.Server_Websocket{Addr: "127.0.0.1:19711"}}),
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
	if n.Key != "server.websocket" || n.Phase != appkit.PhaseServe {
		t.Fatalf("node = %+v, want Key=server.websocket Phase=PhaseServe", n)
	}
	if len(n.Segments) != 1 || n.Segments[0] != "server.websocket" {
		t.Fatalf("node segments = %v, want [server.websocket]", n.Segments)
	}
}

func TestAppProvider_BuildConstructsServer(t *testing.T) {
	srv, cleanup, err := AppProvider().Build(context.Background(),
		*wsBootstrap(&bootstrapv1.Server{Websocket: &bootstrapv1.Server_Websocket{Addr: "127.0.0.1:19712"}}),
		appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil (websocket factory yields no cleanup)")
	}
	got, ok := srv.(*ws.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *websocket.Server", srv)
	}
	if ep := got.Endpoint(); ep != "ws://127.0.0.1:19712" {
		t.Fatalf("Endpoint() = %q, want contract addr on the built server", ep)
	}
}

func TestAppProvider_AddrRequiredFailsFast(t *testing.T) {
	_, _, err := AppProvider().Build(context.Background(),
		*wsBootstrap(&bootstrapv1.Server{Websocket: &bootstrapv1.Server_Websocket{}}),
		appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "server.websocket.addr is required") {
		t.Fatalf("err = %v, want addr required", err)
	}
}

func TestAppProvider_SubSectionMissingSkips(t *testing.T) {
	srv, cleanup, err := AppProvider().Build(context.Background(),
		*wsBootstrap(&bootstrapv1.Server{}), appkit.Node{})
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
		WithServerOptions(ws.WithPath("/hooked")),
		WithAfterBuild(func(context.Context, *ws.Server) error { hooked = true; return nil }),
	).Build(context.Background(),
		*wsBootstrap(&bootstrapv1.Server{Websocket: &bootstrapv1.Server_Websocket{Addr: "127.0.0.1:19713"}}),
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
