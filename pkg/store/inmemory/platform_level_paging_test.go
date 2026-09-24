package inmemory_test

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/kalandramo/bald/pkg/store/inmemory"
)

// TestStore_PlatformLevel_ListWithPagingSkipsIsolation 锁定 WithPlatformLevel
// 对 **ListWithPaging** 同样生效（2026-09-24，暗雷修复）。
//
// 背景（为什么这条测试必须存在）：
//   - `applyIsolation`（store.go:293）有 `if !s.opts.platformLevel` 守卫；
//   - 但 `translate`（store.go:413，ListWithPaging 的翻译入口）**直接调
//     mergeTenant，无任何守卫**。
//
// 两条读路径不对称 → 声明平台级的 Store 若改用 ListWithPaging，仍会被注入
// `tenant_id = ?`。对**无 tenant_id 列**的平台级表（如 bald-admin 的
// Menu/Language/Permission/Role），这会直接 `no such column: tenant_id`。
//
// 本测试用 platformDoc（**无 TenantID 字段**，对齐真实平台级表）锁定：
// 声明 WithPlatformLevel 后，ListWithPaging 必须与 Count/List/Get 一样豁免。
func TestStore_PlatformLevel_ListWithPagingSkipsIsolation(t *testing.T) {
	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
	t.Cleanup(func() { store.UnregisterTenant("tenant_id") })

	p := inmemory.NewProvider[platformDoc](func(d *platformDoc) string { return d.ID })
	s := store.NewStore[platformDoc](p, store.WithPlatformLevel[platformDoc]())
	bg := context.Background()

	for _, d := range []*platformDoc{{ID: "menu-1", Name: "系统管理"}, {ID: "menu-2", Name: "日志"}} {
		if err := s.Create(bg, d); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	// 带租户 ctx 走 ListWithPaging：平台级表应看到全量（2 条）。
	t1 := contextx.WithTenantID(bg, "t-1")
	res, err := s.ListWithPaging(t1, &storev1.PagingRequest{
		Page:     storev1p(1),
		PageSize: storev1p(10),
	})
	if err != nil {
		t.Fatalf("ListWithPaging: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("平台级表 ListWithPaging 返回 %d 条, want 2（豁免应生效，不应被 tenant_id 收窄）", len(res.Items))
	}
	if res.Meta.GetTotal().GetValue() != 2 {
		t.Fatalf("平台级表 ListWithPaging total=%d, want 2", res.Meta.GetTotal().GetValue())
	}
}

// TestStore_PlatformLevel_ListWithPaging_NonPlatformStillIsolated 对照锁定：
// **未**声明平台级的实体走 ListWithPaging 仍被隔离（防止豁免扩大化）。
func TestStore_PlatformLevel_ListWithPaging_NonPlatformStillIsolated(t *testing.T) {
	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
	t.Cleanup(func() { store.UnregisterTenant("tenant_id") })

	p := inmemory.NewProvider[tenantDoc](func(d *tenantDoc) string { return d.ID })
	s := store.NewStore[tenantDoc](p) // 无 WithPlatformLevel
	bg := context.Background()
	_ = s.Create(bg, &tenantDoc{ID: "1", TenantID: "t-1", Name: "a"})
	_ = s.Create(bg, &tenantDoc{ID: "2", TenantID: "t-2", Name: "b"})

	t1 := contextx.WithTenantID(bg, "t-1")
	res, err := s.ListWithPaging(t1, &storev1.PagingRequest{
		Page:     storev1p(1),
		PageSize: storev1p(10),
	})
	if err != nil {
		t.Fatalf("ListWithPaging: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ID != "1" {
		t.Fatalf("未豁免实体 ListWithPaging 应只返回本租户 1 条, got %d 条", len(res.Items))
	}
}
