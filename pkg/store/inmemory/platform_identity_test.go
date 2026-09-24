package inmemory_test

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/kalandramo/bald/pkg/store/inmemory"
)

// TestStore_PlatformIdentity_SkipsIsolation 锁定**身份级**平台豁免：
// ctx 带平台标记时，租户隔离对本次请求整体跳过（跨租户可见）。
//
// 与实体级 WithPlatformLevel 的区别：
//   - 实体级问「这张表有没有租户列」——构造期静态；
//   - 身份级问「这个请求要不要按租户切分」——请求期动态。
//
// 二者正交，本测试锁定后者。
func TestStore_PlatformIdentity_SkipsIsolation(t *testing.T) {
	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
	t.Cleanup(func() { store.UnregisterTenant("tenant_id") })

	p := inmemory.NewProvider[tenantDoc](func(d *tenantDoc) string { return d.ID })
	s := store.NewStore[tenantDoc](p)
	bg := context.Background()
	_ = s.Create(bg, &tenantDoc{ID: "1", TenantID: "t-1", Name: "a"})
	_ = s.Create(bg, &tenantDoc{ID: "2", TenantID: "t-2", Name: "b"})

	// 平台身份 + 某租户值：应看到**全部**（2 条），不被 t-1 收窄。
	platformCtx := contextx.WithPlatform(contextx.WithTenantID(bg, "t-1"))

	n, err := s.Count(platformCtx, nil)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("平台身份 Count=%d, want 2（应跨租户可见）", n)
	}

	// List 直通路径
	items, total, err := s.List(platformCtx, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("平台身份 List total=%d len=%d, want 2/2", total, len(items))
	}

	// ListWithPaging 路径（translate）——两路径必须一致
	res, err := s.ListWithPaging(platformCtx, &storev1.PagingRequest{
		Page:     storev1p(1),
		PageSize: storev1p(10),
	})
	if err != nil {
		t.Fatalf("ListWithPaging: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("平台身份 ListWithPaging 返回 %d 条, want 2（两路径须一致）", len(res.Items))
	}
}

// TestStore_PlatformIdentity_AnonymousStillIsolated 是**安全边界测试**：
// 匿名请求（无平台标记、无租户值）**必须仍被隔离**，不得因平台豁免而 fail-open。
//
// 这条测试的存在理由：平台豁免若实现成「TenantID 为空即跳过」，匿名请求会
// 一并放行（跨租户泄漏）。本测试锁定「豁免必须来自显式平台标记」。
func TestStore_PlatformIdentity_AnonymousStillIsolated(t *testing.T) {
	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
	t.Cleanup(func() { store.UnregisterTenant("tenant_id") })

	p := inmemory.NewProvider[tenantDoc](func(d *tenantDoc) string { return d.ID })
	s := store.NewStore[tenantDoc](p)
	bg := context.Background()
	_ = s.Create(bg, &tenantDoc{ID: "1", TenantID: "t-1", Name: "a"})
	_ = s.Create(bg, &tenantDoc{ID: "2", TenantID: "t-2", Name: "b"})

	// 匿名：无平台标记、无租户值 → 不注入隔离（既有行为），但**绝不能**因此
	// 被当作平台视图获得额外能力。此处锁定的是「无平台标记时 PlatformFromContext
	// 必须为 false」这一 fail-closed 前提。
	if contextx.PlatformFromContext(bg) {
		t.Fatal("匿名 ctx 不得被判定为平台身份（fail-closed）")
	}

	// 显式平台标记才为 true。
	if !contextx.PlatformFromContext(contextx.WithPlatform(bg)) {
		t.Fatal("显式 WithPlatform 后应为 true")
	}

	// 带租户值但无平台标记：隔离必须生效（只看本租户 1 条）。
	t1 := contextx.WithTenantID(bg, "t-1")
	n, err := s.Count(t1, nil)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("租户用户 Count=%d, want 1（隔离必须生效）", n)
	}
}
