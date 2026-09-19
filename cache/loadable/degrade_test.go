package loadable

import (
	"context"
	"errors"
	"testing"

	"github.com/kalandramo/bald/cache"
)

// TestGet_DegradeOnError 钉住降级语义（opt-in）：
// 开启 WithDegradeOnError 后，backend 的非 ErrNotFound 错误（如 Redis 停机）
// 不再上抛，而是降级走 loader——缓存故障不放大为业务故障。
//
// 迁移自 contrib/cache-redis 的 TestCache_Get_DegradedOnRedisDown。
func TestGet_DegradeOnError(t *testing.T) {
	failing := &errCache{err: errors.New("redis: connection refused")}
	loaderCalls := 0
	c := New(failing,
		func(context.Context, string) ([]byte, error) {
			loaderCalls++
			return []byte("from-db"), nil
		},
		WithDegradeOnError(),
	)
	defer c.Close()

	got, err := c.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("开启降级后 backend 故障应走 loader，got err=%v", err)
	}
	if string(got) != "from-db" {
		t.Errorf("Get() = %q, want from-db", got)
	}
	if loaderCalls != 1 {
		t.Errorf("loader 调用 %d 次, want 1", loaderCalls)
	}
}

// TestGet_DegradeOnError_StillHonorsErrNotFound 降级模式下未命中语义不变：
// ErrNotFound 本就该走 loader（非降级，是正常 read-through）。
func TestGet_DegradeOnError_StillHonorsErrNotFound(t *testing.T) {
	backend := newMemCache() // 空 cache：Get 返回 ErrNotFound
	loaderCalls := 0
	c := New(backend,
		func(context.Context, string) ([]byte, error) {
			loaderCalls++
			return []byte("loaded"), nil
		},
		WithDegradeOnError(),
	)
	defer c.Close()

	got, err := c.Get(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "loaded" || loaderCalls != 1 {
		t.Errorf("got=%q calls=%d, want loaded/1", got, loaderCalls)
	}
}

// TestGet_DegradeOnError_LoaderErrorStillPropagates 降级不等于吞错：
// loader 自己失败时错误必须上抛（缓存故障可降级，数据源故障不可）。
func TestGet_DegradeOnError_LoaderErrorStillPropagates(t *testing.T) {
	sentinel := errors.New("db: query failed")
	failing := &errCache{err: errors.New("redis: down")}
	c := New(failing,
		func(context.Context, string) ([]byte, error) { return nil, sentinel },
		WithDegradeOnError(),
	)
	defer c.Close()

	_, err := c.Get(context.Background(), "k")
	if !errors.Is(err, sentinel) {
		t.Fatalf("loader 错误应上抛，got %v", err)
	}
}

// TestGet_DefaultStillPropagatesBackendError 默认（不开降级）语义不变：
// 非 ErrNotFound 错误直接上抛，loader 不被调用。
// 与既有 TestGet_BackendErrorPropagates 同义，此处在「降级选项存在后」再钉一次防回归。
func TestGet_DefaultStillPropagatesBackendError(t *testing.T) {
	failing := &errCache{err: errors.New("backend down")}
	c := New(failing, func(context.Context, string) ([]byte, error) {
		t.Fatal("默认模式下 loader 不应被调用")
		return nil, nil
	})
	defer c.Close()

	if _, err := c.Get(context.Background(), "k"); err == nil || errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("默认模式：Get() error = %v, want non-ErrNotFound backend error", err)
	}
}

// TestGet_DegradeOnError_BackfillFailureTolerated 降级路径下回填失败也不阻塞读。
func TestGet_DegradeOnError_BackfillFailureTolerated(t *testing.T) {
	failing := &errCache{err: errors.New("redis: down")}
	c := New(failing,
		func(context.Context, string) ([]byte, error) { return []byte("v"), nil },
		WithDegradeOnError(),
	)
	defer c.Close()

	// errCache.Set 也失败；Get 仍应返回 loader 值。
	got, err := c.Get(context.Background(), "k")
	if err != nil || string(got) != "v" {
		t.Fatalf("回填失败不应阻塞读：got=%q err=%v", got, err)
	}
}
