package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确、Option 嵌入面可用（Apply 追加进声明）；
//   2. 段在 → Build 成功：产物落 map[string]any{"s3": ...}，SDK 客户端
//      非 nil（fail-closed 语义经既有 Provider 保持）；
//   3. 顶层段缺失 → Build 报 bug 级错误；
//   4. 子段缺失 / region 必填 → 复用 Provider 的 fail-fast。
//
// 构造不联网（aws config 本地组装），可离线验证。

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	s3 "github.com/kalandramo/bald/oss/s3"
	"github.com/kalandramo/bald/pkg/appkit"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "storage.s3" {
		t.Fatalf("Segments() = %v, want [storage.s3]", segs)
	}
}

func TestAppProvider_ApplyAddsToDecl(t *testing.T) {
	d := &appkit.Decl{}
	AppProvider().Apply(d)
	if len(d.Providers) != 1 {
		t.Fatalf("decl providers = %d, want 1", len(d.Providers))
	}
}

func TestAppProvider_BuildConstructsStorage(t *testing.T) {
	scope := appkit.BuildScope{Config: &bootstrapv1.BootstrapConfig{
		Storage: &bootstrapv1.Storage{
			S3: &bootstrapv1.Storage_S3{
				Region:    "us-east-1",
				AccessKey: "ak",
				SecretKey: "sk",
			},
		},
	}}
	product, cleanup, err := appProvider{}.Build(context.Background(), scope)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil (aws client has no explicit Close)")
	}
	m, ok := product.(map[string]any)
	if !ok {
		t.Fatalf("product type = %T, want map[string]any", product)
	}
	st, ok := m["s3"].(*s3.Storage)
	if !ok || st == nil {
		t.Fatalf("product[s3] = (%T, %v), want non-nil *s3.Storage", m["s3"], m["s3"])
	}
	if st.SDK() == nil {
		t.Fatal("SDK client = nil, want non-nil")
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
		Storage: &bootstrapv1.Storage{},
	}}
	_, _, err := appProvider{}.Build(context.Background(), scope)
	if err == nil || !strings.Contains(err.Error(), "s3 section is missing") {
		t.Fatalf("err = %v, want s3 section missing", err)
	}
}

func TestAppProvider_RegionRequiredFailsFast(t *testing.T) {
	scope := appkit.BuildScope{Config: &bootstrapv1.BootstrapConfig{
		Storage: &bootstrapv1.Storage{S3: &bootstrapv1.Storage_S3{}},
	}}
	_, _, err := appProvider{}.Build(context.Background(), scope)
	if err == nil || !strings.Contains(err.Error(), "s3.region is required") {
		t.Fatalf("err = %v, want region required", err)
	}
}
