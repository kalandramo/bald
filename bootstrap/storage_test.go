package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// --- StorageRegistry：显式注册与 fail-fast（自 pkg/appkit 迁入 2026-09-15）---

func TestStorageRegistry_FailFast(t *testing.T) {
	sr := NewStorageRegistry()
	sr.MustRegister("minio", func(context.Context, *bootstrapv1.Storage) (any, func(), error) {
		return nil, nil, nil
	})

	// 重名
	err := sr.Register("minio", func(context.Context, *bootstrapv1.Storage) (any, func(), error) {
		return nil, nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	// 空类型 / nil provider
	if err := sr.Register("", nil); err == nil {
		t.Fatal("expected empty type error")
	}
	if err := sr.Register("s3", nil); err == nil {
		t.Fatal("expected nil provider error")
	}
	// Build：契约段 nil → no-op；零段非 nil 契约 → no-op
	clients, cleanup, err := sr.Build(context.Background(), nil)
	if err != nil || clients != nil || cleanup != nil {
		t.Fatalf("nil section should be no-op, got (clients=%v, cleanup=%t, err=%v)", clients, cleanup != nil, err)
	}
	clients, cleanup, err = sr.Build(context.Background(), &bootstrapv1.Storage{})
	if err != nil || clients == nil || len(clients) != 0 || cleanup != nil {
		t.Fatalf("empty section should be no-op, got (clients=%v, cleanup=%t, err=%v)", clients, cleanup != nil, err)
	}
	// Build：段存在但 provider 未注册（用未注册的 s3 段）
	_, _, err = sr.Build(context.Background(), &bootstrapv1.Storage{
		S3: &bootstrapv1.Storage_S3{Region: "us-east-1"},
	})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected not-registered error, got %v", err)
	}
}

// Build：多段并存 + cleanup 逆序回放；第二段失败回滚第一段。
func TestStorageRegistry_BuildMultiAndRollback(t *testing.T) {
	sr := NewStorageRegistry()
	cleaned := []string{}
	sr.MustRegister("minio", func(context.Context, *bootstrapv1.Storage) (any, func(), error) {
		return "minio-storage", func() { cleaned = append(cleaned, "minio") }, nil
	})

	// 多段并存（s3 失败）
	sr.MustRegister("s3", func(context.Context, *bootstrapv1.Storage) (any, func(), error) {
		return nil, nil, errors.New("boom")
	})
	_, _, err := sr.Build(context.Background(), &bootstrapv1.Storage{
		Minio: &bootstrapv1.Storage_Minio{Endpoint: "e"},
		S3:    &bootstrapv1.Storage_S3{Region: "r"},
	})
	if err == nil || !strings.Contains(err.Error(), "build storage s3") {
		t.Fatalf("expected s3 build error, got %v", err)
	}
	if len(cleaned) != 1 || cleaned[0] != "minio" {
		t.Fatalf("minio should be rolled back, cleaned=%v", cleaned)
	}

	// 两段成功：全部返回 + 逆序回放（s3 先于 minio）
	sr2 := NewStorageRegistry()
	sr2.MustRegister("minio", func(context.Context, *bootstrapv1.Storage) (any, func(), error) {
		return "minio-storage", func() { cleaned = append(cleaned, "minio2") }, nil
	})
	sr2.MustRegister("s3", func(context.Context, *bootstrapv1.Storage) (any, func(), error) {
		return "s3-storage", func() { cleaned = append(cleaned, "s3") }, nil
	})
	clients, cleanup, err := sr2.Build(context.Background(), &bootstrapv1.Storage{
		Minio: &bootstrapv1.Storage_Minio{Endpoint: "e"},
		S3:    &bootstrapv1.Storage_S3{Region: "r"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if clients["minio"] != "minio-storage" || clients["s3"] != "s3-storage" {
		t.Fatalf("clients mismatch: %v", clients)
	}
	cleanup()
	if len(cleaned) != 3 || cleaned[1] != "s3" || cleaned[2] != "minio2" {
		t.Fatalf("cleanup order = %v, want [minio s3 minio2]", cleaned)
	}
}
