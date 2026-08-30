package secret

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/pkg/contextx"

	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// TestSecretBiz_TenantIsolation 锁定 M6.3 真实 DAL + 多租户隔离：
// t-default 用户可读本租户 secret，跨租户 s-other-pwd 被 store 隔离为 not found。
func TestSecretBiz_TenantIsolation(t *testing.T) {
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	biz := New(nil) // 缓存禁用（直连 store），隔离逻辑由 store 保证。

	ctxDefault := contextx.WithTenantID(context.Background(), "t-default")
	ctxOther := contextx.WithTenantID(context.Background(), "t-other")

	// t-default 可读本租户 secret。
	got, err := biz.Get(ctxDefault, "s-db-pwd")
	if err != nil {
		t.Fatalf("Get(s-db-pwd) t-default: %v", err)
	}
	if got.Content != "rds-t-default-9f3a2c" {
		t.Fatalf("content mismatch: got %q", got.Content)
	}

	// t-other 用户无法读到 t-default 的 secret（隔离 → 错误）。
	if _, err := biz.Get(ctxOther, "s-db-pwd"); err == nil {
		t.Fatalf("cross-tenant read should be denied but got secret")
	}

	// t-other 可读本租户 secret。
	if _, err := biz.Get(ctxOther, "s-other-pwd"); err != nil {
		t.Fatalf("Get(s-other-pwd) t-other: %v", err)
	}

	// List 仅返回本租户。
	list, err := biz.List(ctxDefault)
	if err != nil {
		t.Fatalf("List t-default: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("t-default should see 2 secrets, got %d", len(list))
	}
	for _, s := range list {
		if s.TenantID != "t-default" {
			t.Fatalf("leaked cross-tenant secret %s (%s)", s.ID, s.TenantID)
		}
	}
}
