package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确、Apply 落声明时**携带工厂**；
//   2. 段在 → Build 成功：产物 *http3.Server，落点即契约地址；
//   3. Plan 声明的节点键/阶段与段路径同源（引擎按此调度）；
//   4. 子段缺失 → (nil, nil, nil) 跳过；addr 必填 fail-fast；
//   5. 顶层 server 段缺失 → bug 级错误；
//   6. WithServerOptions / WithAfterBuild 经 AppProvider 透传生效。
//
// http3.NewServer 仅构造（监听在 Start），离线可实例化。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/transport/http3"
)

func h3Scope(server *bootstrapv1.Server) appkit.ApplyScope {
	return appkit.ApplyScope{Config: &bootstrapv1.BootstrapConfig{Server: server}}
}

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "server.http3" {
		t.Fatalf("Segments() = %v, want [server.http3]", segs)
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
		h3Scope(&bootstrapv1.Server{Http3: &bootstrapv1.Server_Http3{Addr: "127.0.0.1:19811"}}),
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
	if n.Key != "server.http3" || n.Phase != appkit.PhaseServe {
		t.Fatalf("node = %+v, want Key=server.http3 Phase=PhaseServe", n)
	}
	if len(n.Segments) != 1 || n.Segments[0] != "server.http3" {
		t.Fatalf("node segments = %v, want [server.http3]", n.Segments)
	}
}

func TestAppProvider_BuildConstructsServer(t *testing.T) {
	srv, cleanup, err := AppProvider().Build(context.Background(),
		h3Scope(&bootstrapv1.Server{Http3: &bootstrapv1.Server_Http3{Addr: "127.0.0.1:19812"}}),
		appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil (http3 factory yields no cleanup)")
	}
	got, ok := srv.(*http3.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *http3.Server", srv)
	}
	if ep := got.Endpoint(); ep != "127.0.0.1:19812" {
		t.Fatalf("Endpoint() = %q, want contract addr on the built server", ep)
	}
}

func TestAppProvider_AddrRequiredFailsFast(t *testing.T) {
	_, _, err := AppProvider().Build(context.Background(),
		h3Scope(&bootstrapv1.Server{Http3: &bootstrapv1.Server_Http3{}}), appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "server.http3.addr is required") {
		t.Fatalf("err = %v, want addr required", err)
	}
	// 文案必须带上「实现默认 :443」——否则排障者不知道静默抢 443 才是原危害。
	if err != nil && !strings.Contains(err.Error(), ":443") {
		t.Fatalf("err = %v, want attribution to the :443 implementation default", err)
	}
}

func TestAppProvider_SubSectionMissingSkips(t *testing.T) {
	srv, cleanup, err := AppProvider().Build(context.Background(),
		h3Scope(&bootstrapv1.Server{}), appkit.Node{})
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

// TestAppProvider_OptionsPassthrough 业务接线经 AppProvider 的变参透传：
// 这里用 after-build 注册路由 + 业务 Option 覆盖 addr 两路同时判。
func TestAppProvider_OptionsPassthrough(t *testing.T) {
	var hooked bool
	srv, _, err := AppProvider(
		WithServerOptions(http3.WithAddress("127.0.0.1:19814")),
		WithAfterBuild(func(_ context.Context, s *http3.Server) error {
			hooked = true
			s.HandleFunc("/hooked", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
			return nil
		}),
	).Build(context.Background(),
		h3Scope(&bootstrapv1.Server{Http3: &bootstrapv1.Server_Http3{Addr: "127.0.0.1:19813"}}),
		appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !hooked {
		t.Fatal("after-build hook not invoked via AppProvider (options not passed through)")
	}
	if got := srv.(*http3.Server).Endpoint(); got != "127.0.0.1:19814" {
		t.Fatalf("Endpoint() = %q, want business option addr", got)
	}
}

// http3 工厂恒产 nil cleanup，非 nil 分支只能直测适配函数本身。
