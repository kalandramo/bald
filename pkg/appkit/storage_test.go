package appkit

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
)

// --- StorageRegistry：显式注册与 fail-fast ---

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

// --- FromBootstrap 契约装配：阶段 B 构建 → Storage 取用 → 停机 cleanup ---

func TestFromBootstrap_ContractStorageLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	type stubStorage struct{ name string }
	cleaned := false
	sr := NewStorageRegistry()
	sr.MustRegister("minio", func(context.Context, *bootstrapv1.Storage) (any, func(), error) {
		return &stubStorage{name: "minio"}, func() { cleaned = true }, nil
	})

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Storage = &bootstrapv1.Storage{
		Minio: &bootstrapv1.Storage_Minio{Endpoint: "127.0.0.1:9000"},
	}

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)), WithStorageRegistry(sr))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	if _, ok := a.Storage("minio"); ok {
		t.Fatal("Storage should be empty before Run (phase B not reached)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	cli, ok := a.Storage("minio")
	if !ok {
		t.Fatal("storage instance not built from contract")
	}
	if s, ok := cli.(*stubStorage); !ok || s.name != "minio" {
		t.Fatalf("instance type mismatch: %T", cli)
	}
	if all := a.Storages(); len(all) != 1 {
		t.Fatalf("Storages() = %v, want 1 entry", all)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s")
	}
	if !cleaned {
		t.Fatal("storage cleanup should run on shutdown")
	}
}

// 契约 storage 段存在但未接 StorageRegistry → 阶段 B fail-fast。
func TestFromBootstrap_StorageSectionWithoutTable(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Storage = &bootstrapv1.Storage{
		Minio: &bootstrapv1.Storage_Minio{Endpoint: "127.0.0.1:9000"},
	}

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	err = a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "StorageRegistry") {
		t.Fatalf("expected StorageRegistry missing error, got %v", err)
	}
}
