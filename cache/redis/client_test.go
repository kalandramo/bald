package redis

// client_test.go —— 原生 client 暴露契约的钉子：
//   1. Client() 返回与 New 注入的是**同一实例**（接口判等按指针身份）；
//   2. 同一实例是活的：经 Client() 句柄的写操作直接可见于底层 Redis。

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func TestCacheClient_SameInstance(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	c := New(rdb, WithKeyPrefix("app:"))
	if c.Client() != goredis.UniversalClient(rdb) {
		t.Fatal("Client() = different instance than the client passed to New")
	}
}

func TestCacheClient_SameLiveClient(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	c := New(rdb, WithKeyPrefix("app:"))
	ctx := context.Background()
	if err := c.Client().Set(ctx, "raw-key", "v", 0).Err(); err != nil {
		t.Fatalf("Set via Client(): %v", err)
	}
	if !mr.Exists("raw-key") {
		t.Fatal("write via Client() not visible in the underlying server")
	}
}
