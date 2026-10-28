package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确、Option 嵌入面可用（Apply 追加进声明且**携带工厂**）；
//   2. 段在 → Build 成功：产物 transport.Server 进服务器列表；
//   3. 子段缺失 → (nil, nil, nil) 跳过语义；
//   4. 顶层段缺失 → bug 级错误；
//   5. WithJobs 经 AppProvider 透传生效。
//
// cron 无外部依赖（进程内调度器），段在即构造成功。

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/transport/cron"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "server.cron" {
		t.Fatalf("Segments() = %v, want [server.cron]", segs)
	}
}

// Apply 落声明的是携带工厂的接收者本身——零值副本会让 Build 直呼 nil 工厂。
func TestAppProvider_ApplyPreservesFactory(t *testing.T) {
	p := AppProvider()
	d := &appkit.Decl{}
	p.Apply(d)
	if len(d.Providers) != 1 {
		t.Fatalf("decl providers = %d, want 1", len(d.Providers))
	}
	srv, _, err := d.Providers[0].Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{
			Server: &bootstrapv1.Server{Cron: &bootstrapv1.Server_Cron{}},
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
	srv, cleanup, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{
			Server: &bootstrapv1.Server{Cron: &bootstrapv1.Server_Cron{}},
		},
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil (cron holds no external resource)")
	}
	if _, ok := srv.(*cron.Server); !ok {
		t.Fatalf("srv type = %T, want *cron.Server", srv)
	}
}

func TestAppProvider_WithJobsForwarded(t *testing.T) {
	called := false
	ap := AppProvider(WithJobs(func(*cron.Server) error {
		called = true
		return nil
	}))
	srv, _, err := ap.Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{
			Server: &bootstrapv1.Server{Cron: &bootstrapv1.Server_Cron{}},
		},
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if srv == nil || !called {
		t.Fatalf("srv=%v called=%v, want server built and job hook invoked", srv, called)
	}
}

func TestAppProvider_SubSectionMissingSkips(t *testing.T) {
	srv, cleanup, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
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
	_, _, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{},
	}, appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "report this as a bug") {
		t.Fatalf("err = %v, want bug report (engine gates by exact path)", err)
	}
}
