package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/cache"
)

func TestType(t *testing.T) {
	if Type != "redis" {
		t.Errorf("Type = %q, want %q", Type, "redis")
	}
}

// 段缺失 / 必填字段缺失 fail-fast（不触真连）。

func TestProvider_MissingSection(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Cache{})
	if err == nil {
		t.Fatal("Provider with missing redis section should fail")
	}
	want := `cache: type="redis" but redis section is missing`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestProvider_MissingAddr(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Cache{
		Redis: &bootstrapv1.Cache_Redis{KeyPrefix: "app:"},
	})
	if err == nil {
		t.Fatal("Provider with empty redis.addr should fail")
	}
	want := "cache: redis.addr is required"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// miniredis 起真实内存 Redis 验证全流程：Ping→读写→前缀隔离→cleanup 关闭。
// （§0 硬性原则：接真实 Redis，不用 fake client。）
func TestProvider_BuildRoundTripAndCleanup(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	cli, cleanup, err := Provider(context.Background(), &bootstrapv1.Cache{
		Redis: &bootstrapv1.Cache_Redis{Addr: mr.Addr(), KeyPrefix: "app:"},
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
	// 前缀生效：裸 redis 里键为 app:k
	if !mr.Exists("app:k") {
		t.Fatal("key prefix not applied")
	}
	if v, err := c.Get(ctx, "k"); err != nil || string(v) != "v" {
		t.Fatalf("Get = (%q, %v), want (v, nil)", v, err)
	}

	cleanup()
	if _, err := c.Get(ctx, "k"); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("after cleanup Get err = %v, want closed", err)
	}
}

// 连接不可达 fail-fast（Ping 失败即返回错误，不产半成品实例）。
func TestProvider_Unreachable(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Cache{
		Redis: &bootstrapv1.Cache_Redis{Addr: "127.0.0.1:1"}, // 不可达端口
	})
	if err == nil {
		t.Fatal("Provider with unreachable addr should fail")
	}
}
