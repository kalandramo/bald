package contract

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	s3 "github.com/kalandramo/bald/oss/s3"
)

func TestType(t *testing.T) {
	if Type != "s3" {
		t.Errorf("Type = %q, want %q", Type, "s3")
	}
}

// 段缺失 / 必填字段缺失 fail-fast（不触真连）。

func TestProvider_MissingSection(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Storage{})
	if err == nil {
		t.Fatal("Provider with missing s3 section should fail")
	}
	want := `storage: type="s3" but s3 section is missing`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestProvider_MissingRegion(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Storage{
		S3: &bootstrapv1.Storage_S3{Endpoint: "http://127.0.0.1:9000"},
	})
	if err == nil {
		t.Fatal("Provider with empty s3.region should fail")
	}
	want := "storage: s3.region is required"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// 全字段映射（构造不触网：aws-sdk client 构造仅解析配置）。
func TestProvider_Build(t *testing.T) {
	cli, cleanup, err := Provider(context.Background(), &bootstrapv1.Storage{
		S3: &bootstrapv1.Storage_S3{
			Endpoint:       "http://127.0.0.1:9000",
			Region:         "us-east-1",
			AccessKey:      "ak",
			SecretKey:      "sk",
			Token:          "tk",
			UseSsl:         true,
			ForcePathStyle: true,
			Bucket:         "app",
		},
	})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Error("s3 client has no Close semantics, cleanup should be nil")
	}
	if _, ok := cli.(*s3.Storage); !ok {
		t.Fatalf("instance type = %T, want *s3.Storage", cli)
	}
}
