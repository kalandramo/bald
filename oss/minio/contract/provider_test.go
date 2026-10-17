package contract

// provider_test.go —— AppProvider 段驱动导出的最小单元测试：
//   1. 段路径声明正确、Option 嵌入面可用（Apply 追加进声明）；
//   2. 段在 → Build 成功：产物落 map[string]any{"minio": ...}，SDK 客户端
//      非 nil（fail-closed 语义经既有 Provider 保持）；
//   3. 顶层段缺失 → Build 报 bug 级错误；
//   4. 子段缺失 / endpoint 必填 → 复用 Provider 的 fail-fast。
//
// 构造不联网（连接延迟到首次操作），可离线验证。

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	minio "github.com/kalandramo/bald/oss/minio"
	"github.com/kalandramo/bald/pkg/appkit"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "storage.minio" {
		t.Fatalf("Segments() = %v, want [storage.minio]", segs)
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
			Minio: &bootstrapv1.Storage_Minio{
				Endpoint:  "127.0.0.1:9000",
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
		t.Fatal("cleanup != nil, want nil (minio client holds no pool resource)")
	}
	m, ok := product.(map[string]any)
	if !ok {
		t.Fatalf("product type = %T, want map[string]any", product)
	}
	st, ok := m["minio"].(*minio.Storage)
	if !ok || st == nil {
		t.Fatalf("product[minio] = (%T, %v), want non-nil *minio.Storage", m["minio"], m["minio"])
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
	if err == nil || !strings.Contains(err.Error(), "minio section is missing") {
		t.Fatalf("err = %v, want minio section missing", err)
	}
}

func TestAppProvider_EndpointRequiredFailsFast(t *testing.T) {
	scope := appkit.BuildScope{Config: &bootstrapv1.BootstrapConfig{
		Storage: &bootstrapv1.Storage{Minio: &bootstrapv1.Storage_Minio{}},
	}}
	_, _, err := appProvider{}.Build(context.Background(), scope)
	if err == nil || !strings.Contains(err.Error(), "minio.endpoint is required") {
		t.Fatalf("err = %v, want endpoint required", err)
	}
}
