package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试（参数映射面）：
//   1. 段路径声明正确、Option 嵌入面可用（Apply 追加进声明）；
//   2. 顶层段缺失 → Build 报 bug 级错误；
//   3. 子段缺失 / server addrs 必填 / 地址格式非法 → 复用 Provider 的
//      fail-fast（构造参数映射的离线可验证部分）。
//
// 成功构造路径不在此覆盖：nacos v2 SDK 的 naming client 构造即建 gRPC
// 长连接（离线不可行）——连通性与注册行为由既有 registry_test 覆盖，
// 本文件按「段缺失跳过 + 参数映射」策略收敛。

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/appkit"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "registry.nacos" {
		t.Fatalf("Segments() = %v, want [registry.nacos]", segs)
	}
}

func TestAppProvider_ApplyAddsToDecl(t *testing.T) {
	d := &appkit.Decl{}
	AppProvider().Apply(d)
	if len(d.Providers) != 1 {
		t.Fatalf("decl providers = %d, want 1", len(d.Providers))
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
	if err == nil || !strings.Contains(err.Error(), "nacos section is missing") {
		t.Fatalf("err = %v, want nacos section missing", err)
	}
}

func TestAppProvider_ServerAddrsRequiredFailsFast(t *testing.T) {
	scope := appkit.BuildScope{Config: &bootstrapv1.BootstrapConfig{
		Registry: &bootstrapv1.Registry{Nacos: &bootstrapv1.Registry_Nacos{}},
	}}
	_, _, err := appProvider{}.Build(context.Background(), scope)
	if err == nil || !strings.Contains(err.Error(), "server addrs is required") {
		t.Fatalf("err = %v, want server addrs required", err)
	}
}

func TestAppProvider_InvalidAddrFormatFailsFast(t *testing.T) {
	scope := appkit.BuildScope{Config: &bootstrapv1.BootstrapConfig{
		Registry: &bootstrapv1.Registry{
			Nacos: &bootstrapv1.Registry_Nacos{ServerAddrs: []string{"no-port"}},
		},
	}}
	_, _, err := appProvider{}.Build(context.Background(), scope)
	if err == nil || !strings.Contains(err.Error(), "invalid server addr") {
		t.Fatalf("err = %v, want invalid server addr", err)
	}
}
