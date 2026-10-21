package authzcasbin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	fileadapter "github.com/casbin/casbin/v2/persist/file-adapter"
)

// mustRowsAuthorizer 构造 NewRows 授权器（p: admin→secret get；g: u-1→admin）。
func mustRowsAuthorizer(t *testing.T) *Authorizer {
	t.Helper()
	a, err := NewRows([]PolicyRow{{Role: "admin", Object: "secret", Action: "get"}},
		[]Grant{{Subject: "u-1", Role: "admin"}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// cache_test.go 为判定结果缓存（cache.go）的语义测试。
// 安全红线逐条落测：
//   - 命中/未命中判定与引擎一致（不放大放行面）；
//   - Reload 后缓存必空——**陈旧放行必须失效**（stale allow → deny）；
//   - 失败的 Reload 也失效（旧策略仍在 → 判定仍正确）；
//   - 容量有界（LRU 逐出，map 尺寸有上界）；
//   - 并发（Authorize × Reload）无数据竞争（-race 下有意义）。

// TestEnforceCache_LRUBoundedCapacity 容量有界：插入数超容量时逐出最久未用，
// map 尺寸不超上界；被逐出的键再次查询为未命中（可重评估）。
func TestEnforceCache_LRUBoundedCapacity(t *testing.T) {
	c := newEnforceCache(2)
	k1 := enforceKey{subject: "u-1", object: "o-1", action: "get"}
	k2 := enforceKey{subject: "u-2", object: "o-2", action: "get"}
	k3 := enforceKey{subject: "u-3", object: "o-3", action: "get"}

	c.put(k1, true)
	c.put(k2, false)
	if len(c.items) != 2 {
		t.Fatalf("容量内条目数 = %d, want 2", len(c.items))
	}
	// 触及 k1（k2 成为最久未用），再插入 k3 应逐出 k2。
	if _, ok := c.get(k1); !ok {
		t.Fatal("k1 应命中")
	}
	c.put(k3, true)
	if len(c.items) != 2 {
		t.Fatalf("逐出后条目数 = %d, want 2（有界）", len(c.items))
	}
	if _, ok := c.get(k2); ok {
		t.Fatal("k2（最久未用）应被逐出")
	}
	if allowed, ok := c.get(k1); !ok || !allowed {
		t.Fatalf("k1 应保留且 allowed=true, got ok=%v allowed=%v", ok, allowed)
	}
	// 容量 <1 的防御：退化为 1 条仍工作。
	c1 := newEnforceCache(0)
	c1.put(k1, false)
	c1.put(k2, true)
	if len(c1.items) != 1 {
		t.Fatalf("退化容量条目数 = %d, want 1", len(c1.items))
	}
}

// TestEnforceCache_Invalidate 整体失效后全部未命中。
func TestEnforceCache_Invalidate(t *testing.T) {
	c := newEnforceCache(4)
	for i := 0; i < 4; i++ {
		c.put(enforceKey{subject: fmt.Sprintf("u-%d", i), object: "o", action: "get"}, true)
	}
	c.invalidate()
	for i := 0; i < 4; i++ {
		if _, ok := c.get(enforceKey{subject: fmt.Sprintf("u-%d", i), object: "o", action: "get"}); ok {
			t.Fatalf("失效后 u-%d 不应命中", i)
		}
	}
	if len(c.items) != 0 {
		t.Fatalf("失效后条目数 = %d, want 0", len(c.items))
	}
}

// TestAuthorizer_CacheVerdictConsistent 命中判定与引擎一致：放行保持放行、
// 拒绝保持拒绝（缓存不放大放行面）；键含 ToLower 后的 action（大小写变体
// 共享同一判定）。
func TestAuthorizer_CacheVerdictConsistent(t *testing.T) {
	a := mustRowsAuthorizer(t)
	ctx := context.Background()

	// 放行判定两次（第二次命中缓存），结果一致。
	for i := 0; i < 2; i++ {
		ok, err := a.Authorize(ctx, "u-1", "secret", "get")
		if err != nil || !ok {
			t.Fatalf("u-1/secret/get 第 %d 次: ok=%v err=%v", i+1, ok, err)
		}
	}
	// 拒绝判定两次，结果一致（fail-closed 不因缓存放宽）。
	for i := 0; i < 2; i++ {
		ok, err := a.Authorize(ctx, "u-2", "secret", "get")
		if err != nil || ok {
			t.Fatalf("u-2/secret/get 第 %d 次: ok=%v err=%v", i+1, ok, err)
		}
	}
	// 大小写变体：GET 命中 get 的缓存判定。
	ok, err := a.Authorize(ctx, "u-1", "secret", "GET")
	if err != nil || !ok {
		t.Fatalf("u-1/secret/GET（大写变体）: ok=%v err=%v", ok, err)
	}
	if len(a.cache.items) != 2 {
		t.Fatalf("缓存键数 = %d, want 2（allow 与 deny 各一，大小写共享键）", len(a.cache.items))
	}
}

// TestAuthorizer_ReloadInvalidatesStaleAllow 安全红线（最关键方向）：缓存中的
// **放行**判定，在策略收回授权并 Reload 后必须失效——陈旧放行不得越过重载生效。
func TestAuthorizer_ReloadInvalidatesStaleAllow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.csv")
	if err := os.WriteFile(path, []byte("p, admin, secret, get\ng, u-1, admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := NewWithAdapter(modelConf, fileadapter.NewAdapter(path))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if ok, _ := a.Authorize(ctx, "u-1", "secret", "get"); !ok {
		t.Fatal("前置：u-1 应放行（并进缓存）")
	}

	// 运行期收回授权：删除 g 行，Reload。
	if err := os.WriteFile(path, []byte("p, admin, secret, get\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(a.cache.items) != 0 {
		t.Fatalf("Reload 后缓存应空, got %d 条", len(a.cache.items))
	}
	if ok, _ := a.Authorize(ctx, "u-1", "secret", "get"); ok {
		t.Fatal("收回授权并 Reload 后 u-1 应拒绝（陈旧放行必须失效）")
	}
}

// TestAuthorizer_FailedReloadKeepsVerdicts 装载失败路径：LoadPolicy 失败时旧
// 策略仍在（casbin fail-safe），缓存已失效——重新评估的判定与旧策略一致。
func TestAuthorizer_FailedReloadKeepsVerdicts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.csv")
	if err := os.WriteFile(path, []byte("p, admin, secret, get\ng, u-1, admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := NewWithAdapter(modelConf, fileadapter.NewAdapter(path))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if ok, _ := a.Authorize(ctx, "u-1", "secret", "get"); !ok {
		t.Fatal("前置：u-1 应放行")
	}

	// 写入字段数非法的策略行（p 行 3 字段 < 定义 4 字段）→ LoadPolicy 报错。
	if err := os.WriteFile(path, []byte("p, admin, secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Reload(); err == nil {
		t.Fatal("非法策略行的 Reload 应报错")
	}
	if len(a.cache.items) != 0 {
		t.Fatalf("失败的 Reload 也应失效缓存, got %d 条", len(a.cache.items))
	}
	// 旧策略仍在（casbin fail-safe）→ 重新评估仍放行。
	if ok, err := a.Authorize(ctx, "u-1", "secret", "get"); err != nil || !ok {
		t.Fatalf("失败 Reload 后旧策略应仍在: ok=%v err=%v", ok, err)
	}
}

// TestAuthorizer_CacheCapacityThroughAuthorize 经 API 的容量上界：键空间远超
// 缓存容量时 map 尺寸有上界，且逐出后重新评估的判定仍正确。
func TestAuthorizer_CacheCapacityThroughAuthorize(t *testing.T) {
	a := mustRowsAuthorizer(t)
	a.cache = newEnforceCache(4) // 替换为小容量缓存以观测逐出
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		if _, err := a.Authorize(ctx, fmt.Sprintf("u-%d", i), "secret", "get"); err != nil {
			t.Fatal(err)
		}
		if len(a.cache.items) > 4 {
			t.Fatalf("条目数 %d 超过容量 4", len(a.cache.items))
		}
	}
	// 逐出后重新评估：判定与策略一致（u-1 有绑定 → 放行）。
	if ok, err := a.Authorize(ctx, "u-1", "secret", "get"); err != nil || !ok {
		t.Fatalf("逐出后重评估: ok=%v err=%v", ok, err)
	}
}

// TestAuthorizer_CacheConcurrentWithReload 并发安全：多 subject 判定与并发
// Reload 交叉（get/put/invalidate 竞争面）在 -race 下无数据竞争，终态正确。
func TestAuthorizer_CacheConcurrentWithReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.csv")
	if err := os.WriteFile(path, []byte("p, admin, secret, get\ng, u-1, admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := NewWithAdapter(modelConf, fileadapter.NewAdapter(path))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
					sub := fmt.Sprintf("u-%d", (w*13+i)%16) // 16 个 subject 循环（含未绑定者）
					_, _ = a.Authorize(ctx, sub, "secret", "get")
				}
			}
		}(w)
	}
	for i := 0; i < 20; i++ {
		if err := a.Reload(); err != nil {
			t.Errorf("Reload: %v", err)
			break
		}
	}
	close(stop)
	wg.Wait()

	if ok, _ := a.Authorize(ctx, "u-1", "secret", "get"); !ok {
		t.Error("并发重载后 u-1 绑定应仍在")
	}
	if ok, _ := a.Authorize(ctx, "u-7", "secret", "get"); ok {
		t.Error("未绑定的 u-7 应拒绝")
	}
}

// TestAuthorizer_ErrorNotCached 引擎错误不入缓存：空 subject 报错（评估前拒绝）
// 不产生缓存条目——错误永不成为命中结果。
func TestAuthorizer_ErrorNotCached(t *testing.T) {
	a := mustRowsAuthorizer(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := a.Authorize(ctx, "", "secret", "get"); err == nil {
			t.Fatal("空 subject 应报错")
		}
	}
	if len(a.cache.items) != 0 {
		t.Fatalf("报错路径不应产生缓存条目, got %d 条", len(a.cache.items))
	}
}
