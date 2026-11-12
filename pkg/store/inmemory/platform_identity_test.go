package inmemory_test

import (
	"context"
	"errors"
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
	p := inmemory.NewProvider[tenantDoc](func(d *tenantDoc) string { return d.ID })
	s := store.NewStore[tenantDoc](p)
	bg := context.Background()
	// 先播种（未注册维度），再开启隔离。
	_ = s.Create(bg, &tenantDoc{ID: "1", TenantID: "t-1", Name: "a"})
	_ = s.Create(bg, &tenantDoc{ID: "2", TenantID: "t-2", Name: "b"})

	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
	t.Cleanup(func() { store.UnregisterTenant("tenant_id") })

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
// ⚠️ 本测试是本次审计中「名字撒谎」的假闸门之一：原版本函数名叫
// `AnonymousStillIsolated`，函数体却只断言 `PlatformFromContext(bg)==false`，
// **从未断言匿名读被隔离**，注释还自认「不注入隔离（既有行为）」。
// 名字承诺的事一件都没验。
//
// 现在补上真断言：多租户应用里匿名请求一律 ErrMissingTenant。
func TestStore_PlatformIdentity_AnonymousStillIsolated(t *testing.T) {
	p := inmemory.NewProvider[tenantDoc](func(d *tenantDoc) string { return d.ID })
	s := store.NewStore[tenantDoc](p)
	bg := context.Background()
	// 先播种（未注册维度），再开启隔离。
	_ = s.Create(bg, &tenantDoc{ID: "1", TenantID: "t-1", Name: "a"})
	_ = s.Create(bg, &tenantDoc{ID: "2", TenantID: "t-2", Name: "b"})

	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
	t.Cleanup(func() { store.UnregisterTenant("tenant_id") })

	// 1) 匿名 ctx 不得被判定为平台身份（fail-closed 前提）。
	if contextx.PlatformFromContext(bg) {
		t.Fatal("匿名 ctx 不得被判定为平台身份（fail-closed）")
	}
	// 2) 显式平台标记才为 true。
	if !contextx.PlatformFromContext(contextx.WithPlatform(bg)) {
		t.Fatal("显式 WithPlatform 后应为 true")
	}

	// 3) **真断言**：匿名（无租户值、无平台标记）必须被拒绝，而不是放行看全表。
	if _, err := s.Count(bg, nil); !errors.Is(err, store.ErrMissingTenant) {
		t.Fatalf("匿名请求 Count err=%v, want ErrMissingTenant（隔离必须生效）", err)
	}
	if _, _, err := s.List(bg, nil); !errors.Is(err, store.ErrMissingTenant) {
		t.Fatalf("匿名请求 List err=%v, want ErrMissingTenant", err)
	}

	// 4) 带租户值但无平台标记：隔离生效，只看本租户 1 条。
	t1 := contextx.WithTenantID(bg, "t-1")
	n, err := s.Count(t1, nil)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("租户用户 Count=%d, want 1（隔离必须生效）", n)
	}
}
