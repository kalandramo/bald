package contract

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	minio "github.com/kalandramo/bald/oss/minio"
)

func TestType(t *testing.T) {
	if Type != "minio" {
		t.Errorf("Type = %q, want %q", Type, "minio")
	}
}

// 段缺失 / 必填字段缺失 fail-fast（不触真连）。

func TestProvider_MissingSection(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Storage{})
	if err == nil {
		t.Fatal("Provider with missing minio section should fail")
	}
	want := `storage: type="minio" but minio section is missing`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestProvider_MissingEndpoint(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Storage{
		Minio: &bootstrapv1.Storage_Minio{AccessKey: "ak"},
	})
	if err == nil {
		t.Fatal("Provider with empty minio.endpoint should fail")
	}
	want := "storage: minio.endpoint is required"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// 全字段映射（构造不触网：minio.NewStorage 仅填充客户端结构）。
func TestProvider_Build(t *testing.T) {
	cli, cleanup, err := Provider(context.Background(), &bootstrapv1.Storage{
		Minio: &bootstrapv1.Storage_Minio{
			Endpoint:  "127.0.0.1:9000",
			AccessKey: "ak",
			SecretKey: "sk",
			Token:     "tk",
			UseSsl:    true,
		},
	})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Error("minio client has no pool to close, cleanup should be nil")
	}
	if _, ok := cli.(*minio.Storage); !ok {
		t.Fatalf("instance type = %T, want *minio.Storage", cli)
	}
}

// fail-closed 回归：endpoint 语法非法时 minio.New 会失败，NewClient 返回 nil，
// 而 NewStorage 仍包出非 nil 门面。Provider 必须在此拦下，不得返回 (client, nil, nil)。
// 覆盖 3 个可复现的失败 endpoint（探针实测均为 nil client）。
func TestProvider_ClientConstructionFailureFailsClosed(t *testing.T) {
	for _, ep := range []string{"://bad", "a b c", "bad::host"} {
		cfg := &bootstrapv1.Storage{
			Minio: &bootstrapv1.Storage_Minio{Endpoint: ep},
		}
		cli, cleanup, err := Provider(context.Background(), cfg)
		if err == nil {
			t.Errorf("endpoint=%q: Provider returned nil error, want construction failure", ep)
		}
		if cli != nil {
			t.Errorf("endpoint=%q: Provider returned non-nil instance on failure", ep)
		}
		if cleanup != nil {
			t.Errorf("endpoint=%q: cleanup should be nil on failure", ep)
		}
	}
}
