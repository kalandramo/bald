package appkit

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
)

// 约定装配：阶段 B 构建 → Storage 取用 → 停机 cleanup。

func TestAssemble_ContractStorageLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	type stubStorage struct{ name string }
	cleaned := false
	prov := stubDomainProvider("storage", "minio",
		func(c *bootstrapv1.BootstrapConfig) bool { return c.GetStorage().GetMinio() != nil },
		&stubStorage{name: "minio"}, func() { cleaned = true })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Storage = &bootstrapv1.Storage{
		Minio: &bootstrapv1.Storage_Minio{Endpoint: "127.0.0.1:9000"},
	}

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)), prov)
	if _, ok := a.lookup("storage.minio"); ok {
		t.Fatal("Storage should be empty before Run (phase B not reached)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	cli, ok := a.lookup("storage.minio")
	if !ok {
		t.Fatal("storage instance not built from contract")
	}
	if s, ok := cli.(*stubStorage); !ok || s.name != "minio" {
		t.Fatalf("instance type mismatch: %T", cli)
	}
	if all := resourceMap(t, a, "storage"); len(all) != 1 {
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

// 契约 storage 段存在但无 storage Provider → 阶段 B fail-fast。
func TestAssemble_StorageSectionWithoutTable(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Storage = &bootstrapv1.Storage{
		Minio: &bootstrapv1.Storage_Minio{Endpoint: "127.0.0.1:9000"},
	}

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)))
	err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no consumer") || !strings.Contains(err.Error(), "storage") {
		t.Fatalf("expected no-consumer error for storage, got %v", err)
	}
}
