package authzcasbin

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	fileadapter "github.com/casbin/casbin/v2/persist/file-adapter"

	"github.com/kalandramo/bald/pkg/authz"
)

// ---------------------------------------------------------------------------
// D7：casbin 授权器的运行期策略重载
//
// 缺陷：策略在构造期一次性装载，新注册用户的角色绑定（g 行）不在其中 →
// 注册后立即访问受保护资源返回 403，须重启才生效。
// 修法：实现 authz.ReloadableAuthorizer，从策略源重新装载（写锁独占）。
// ---------------------------------------------------------------------------

const modelConf = `
[request_definition]
r = sub, obj, act
[policy_definition]
p = sub, obj, act
[role_definition]
g = _, _
[policy_effect]
e = some(where (p.eft == allow))
[matchers]
m = g(r.sub, p.sub) && r.obj == p.obj && r.act == p.act
`

// writePolicy 把策略文本写入临时 csv，返回路径。
func writePolicy(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "policy.csv")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCasbin_ReloadPicksUpNewRoleBinding 核心验收（D7 端到端语义）：
// 策略文件新增 subject→角色 绑定时，Reload 后新的判定立即生效——无需重建授权器。
func TestCasbin_ReloadPicksUpNewRoleBinding(t *testing.T) {
	dir := t.TempDir()
	// 初始：只有 u-1 是 admin。
	csv := "p, admin, secret, get\ng, u-1, admin\n"
	path := writePolicy(t, dir, csv)

	a, err := NewWithAdapter(modelConf, fileadapter.NewAdapter(path))
	if err != nil {
		t.Fatalf("NewWithAdapter: %v", err)
	}
	if _, ok := interface{}(a).(authz.ReloadableAuthorizer); !ok {
		t.Fatal("*Authorizer 应实现 authz.ReloadableAuthorizer")
	}

	ctx := context.Background()

	// 前置：u-1 可读，u-2 不可（尚无绑定）。
	if ok, err := a.Authorize(ctx, "u-1", "secret", "get"); err != nil || !ok {
		t.Fatalf("前置 u-1 应放行: ok=%v err=%v", ok, err)
	}
	if ok, _ := a.Authorize(ctx, "u-2", "secret", "get"); ok {
		t.Fatal("前置 u-2 无绑定应拒绝")
	}

	// 模拟运行期变更：新用户 u-2 注册并绑定 admin。
	csv += "g, u-2, admin\n"
	if err := os.WriteFile(path, []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}

	// 重载前仍拒绝（证明生效来自 Reload，而非文件写入本身）。
	if ok, _ := a.Authorize(ctx, "u-2", "secret", "get"); ok {
		t.Fatal("Reload 前 u-2 仍应拒绝")
	}

	if err := a.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// Reload 后 u-2 立即放行——D7 的核心收益。
	ok, err := a.Authorize(ctx, "u-2", "secret", "get")
	if err != nil {
		t.Fatalf("Reload 后判定出错: %v", err)
	}
	if !ok {
		t.Fatal("Reload 后 u-2 应放行（运行期策略变更生效）")
	}
	// 既有绑定不受影响。
	if ok, _ := a.Authorize(ctx, "u-1", "secret", "get"); !ok {
		t.Error("Reload 不应丢失既有绑定 u-1")
	}
}

// TestCasbin_ReloadViaHelper 通过 authz.Reload 助手发现并调用（调用方统一入口）。
func TestCasbin_ReloadViaHelper(t *testing.T) {
	dir := t.TempDir()
	path := writePolicy(t, dir, "p, admin, secret, get\n")
	a, err := NewWithAdapter(modelConf, fileadapter.NewAdapter(path))
	if err != nil {
		t.Fatal(err)
	}
	supported, err := authz.Reload(a)
	if !supported {
		t.Fatal("助手应发现 casbin 授权器支持重载")
	}
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
}

// TestCasbin_ReloadEmptyPolicyIsNoOp 空策略初始态（无适配器）下 Reload 为合法无操作。
func TestCasbin_ReloadEmptyPolicyIsNoOp(t *testing.T) {
	a, err := NewWithModel(modelConf, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Reload(); err != nil {
		t.Fatalf("无源可重载时应返回 nil，got %v", err)
	}
	// fail-closed：无策略时一律拒绝。
	if ok, _ := a.Authorize(context.Background(), "u-1", "secret", "get"); ok {
		t.Error("空策略应 fail-closed（拒绝）")
	}
}

// TestCasbin_ReloadConcurrentWithAuthorize 并发安全：Reload 与 Authorize 并发
// 不构成数据竞争（本测试在 -race 下有意义）。
func TestCasbin_ReloadConcurrentWithAuthorize(t *testing.T) {
	dir := t.TempDir()
	path := writePolicy(t, dir, "p, admin, secret, get\ng, u-1, admin\n")
	a, err := NewWithAdapter(modelConf, fileadapter.NewAdapter(path))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 并发判定。
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = a.Authorize(ctx, "u-1", "secret", "get")
				}
			}
		}()
	}
	// 并发重载。
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
}
