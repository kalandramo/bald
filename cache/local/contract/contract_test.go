package contract

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/cache"
)

func TestType(t *testing.T) {
	if Type != "local" {
		t.Errorf("Type = %q, want %q", Type, "local")
	}
}

func TestProvider_MissingSection(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Cache{})
	if err == nil {
		t.Fatal("Provider with missing local section should fail")
	}
	want := `cache: type="local" but local section is missing`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// 全字段映射 + cleanup 语义（Close 清空内存）。
func TestProvider_BuildAndCleanup(t *testing.T) {
	cli, cleanup, err := Provider(context.Background(), &bootstrapv1.Cache{
		Local: &bootstrapv1.Cache_Local{Size: 1024 * 1024, DefaultTtlSeconds: 60},
	})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	c, ok := cli.(cache.Cache)
	if !ok {
		t.Fatalf("client type = %T, want cache.Cache", cli)
	}
	ctx := context.Background()
	if err := c.Set(ctx, "k", []byte("v"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, err := c.Get(ctx, "k"); err != nil || string(v) != "v" {
		t.Fatalf("Get = (%q, %v), want (v, nil)", v, err)
	}
	cleanup()
	if _, err := c.Get(ctx, "k"); err != cache.ErrNotFound {
		t.Fatalf("after cleanup Get err = %v, want ErrNotFound", err)
	}
}

// buildOptions 纯函数映射断言。
func TestBuildOptions(t *testing.T) {
	if opts := buildOptions(&bootstrapv1.Cache_Local{}); len(opts) != 0 {
		t.Fatalf("empty section options = %d, want 0", len(opts))
	}
	opts := buildOptions(&bootstrapv1.Cache_Local{Size: 1024, DefaultTtlSeconds: 30})
	if len(opts) != 2 {
		t.Fatalf("full options = %d, want 2", len(opts))
	}
}
