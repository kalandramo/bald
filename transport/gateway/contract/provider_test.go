package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确、Option 嵌入面可用（Apply 追加进声明且**携带工厂**）；
//   2. 段在 → Build 成功：产物 transport.Server 进服务器列表；
//   3. 子段缺失 → (nil, nil, nil) 跳过语义；register nil / addr 必填 fail-fast；
//   4. 顶层段缺失 → bug 级错误。
//
// NewGatewayServer 仅构造（连接与监听延迟到 Start），离线可实例化。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/appkit"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider(nil).Segments()
	if len(segs) != 1 || segs[0] != "server.gateway" {
		t.Fatalf("Segments() = %v, want [server.gateway]", segs)
	}
}

// Apply 落声明的是携带工厂的接收者本身——零值副本会让 Build 直呼 nil 工厂。
func TestAppProvider_ApplyPreservesFactory(t *testing.T) {
	p := AppProvider(func(context.Context, *grpc.ClientConn) (http.Handler, error) {
		return http.NewServeMux(), nil
	})
	d := &appkit.Decl{}
	p.Apply(d)
	if len(d.Providers) != 1 {
		t.Fatalf("decl providers = %d, want 1", len(d.Providers))
	}
	srv, _, err := d.Providers[0].Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{
			Server: &bootstrapv1.Server{
				Gateway: &bootstrapv1.Server_Gateway{
					Addr:            "127.0.0.1:18081",
					BackendGrpcAddr: "127.0.0.1:19090",
				},
			},
		},
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build via decl: %v", err)
	}
	if srv == nil {
		t.Fatal("srv = nil via decl provider, factory was lost in Apply")
	}
}

func TestAppProvider_BuildConstructsServer(t *testing.T) {
	srv, cleanup, err := AppProvider(func(context.Context, *grpc.ClientConn) (http.Handler, error) {
		return http.NewServeMux(), nil
	}).Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{
			Server: &bootstrapv1.Server{
				Gateway: &bootstrapv1.Server_Gateway{
					Addr:            "127.0.0.1:18081",
					BackendGrpcAddr: "127.0.0.1:19090",
				},
			},
		},
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil (gateway conn lifecycle belongs to Start/Stop)")
	}
	if srv == nil {
		t.Fatal("srv = nil, want *gateway.GatewayServer")
	}
}

func TestAppProvider_RegisterNilFailsFast(t *testing.T) {
	_, _, err := AppProvider(nil).Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{
			Server: &bootstrapv1.Server{
				Gateway: &bootstrapv1.Server_Gateway{Addr: "127.0.0.1:18081"},
			},
		},
	}, appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "no register func provided") {
		t.Fatalf("err = %v, want register func required", err)
	}
}

func TestAppProvider_AddrRequiredFailsFast(t *testing.T) {
	_, _, err := AppProvider(func(context.Context, *grpc.ClientConn) (http.Handler, error) {
		return http.NewServeMux(), nil
	}).Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{
			Server: &bootstrapv1.Server{
				Gateway: &bootstrapv1.Server_Gateway{BackendGrpcAddr: "127.0.0.1:19090"},
			},
		},
	}, appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "server.gateway.addr is required") {
		t.Fatalf("err = %v, want addr required", err)
	}
}

func TestAppProvider_SubSectionMissingSkips(t *testing.T) {
	srv, cleanup, err := AppProvider(nil).Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{Server: &bootstrapv1.Server{}},
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build error = %v, want nil (skip)", err)
	}
	if srv != nil || cleanup != nil {
		t.Fatalf("(srv, cleanup) = (%v, %v), want (nil, nil) on skip", srv, cleanup)
	}
}

func TestAppProvider_TopSectionMissingIsBug(t *testing.T) {
	_, _, err := AppProvider(nil).Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{},
	}, appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "report this as a bug") {
		t.Fatalf("err = %v, want bug report (engine gates by exact path)", err)
	}
}
