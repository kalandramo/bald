package appkit

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
	"github.com/kalandramo/bald/log"
)

// FromBootstrap 契约装配：阶段 B 构建 → Storage 取用 → 停机 cleanup。
// Registry 行为测试（注册/段枚举/回滚）已随六域 Registry 迁 bootstrap
// （bootstrap/storage_test.go，2026-09-15）。

func TestFromBootstrap_ContractStorageLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	type stubStorage struct{ name string }
	cleaned := false
	sr := baldbootstrap.NewStorageRegistry()
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
