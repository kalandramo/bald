package inmemory_test

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/kalandramo/bald/pkg/store/inmemory"
)

// platformDoc 是**平台级**测试实体——无 tenant_id 字段（对齐 bald-admin 的
// Menu/Language/Permission/Role：全平台共享数据，表里没有租户维度）。
type platformDoc struct {
	ID   string
	Name string
}

// TestStore_PlatformLevel_SkipsTenantIsolation 锁定 WithPlatformLevel 豁免
// （2026-09-24，缺陷 D20）。
//
// 背景：b04dc78 起 Get/List/Count/Delete 无条件调 applyIsolation → mergeTenant
// 自动注入 `tenant_id = ?`。对**无 tenant_id 列**的平台级表，这会产生
// `no such column: tenant_id`（bald-admin 实测 13 个 e2e 500）。
//
// 本测试在 inmemory 后端锁定「豁免生效」：声明 WithPlatformLevel 后，带租户 ctx
// 的查询**不再被租户条件收窄**（本实体也无 TenantID 字段可供收窄）。
func TestStore_PlatformLevel_SkipsTenantIsolation(t *testing.T) {
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

	// 带租户 ctx：平台级表应看到**全量**（不被 tenant_id 收窄）。
	t1 := contextx.WithTenantID(bg, "t-1")

	n, err := s.Count(t1, nil)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("平台级表 Count=%d, want 2（豁免生效：不应被租户条件收窄）", n)
	}

	items, total, err := s.List(t1, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("平台级表 List total=%d len=%d, want 2/2", total, len(items))
	}

	// Get 按主键：平台级表应命中。
	if _, err := s.Get(t1, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "menu-1")}}); err != nil {
		t.Fatalf("平台级表 Get 应命中: %v", err)
	}

	// 对照：未声明平台级的同名实体，带租户 ctx 会被注入租户条件 →
	// 本实体无 TenantID 字段，字段值取空串，与 "t-1" 不等 → 全部过滤掉。
	// 这正是「不豁免即被隔离」的反证。
	p2 := inmemory.NewProvider[platformDoc](func(d *platformDoc) string { return d.ID })
	s2 := store.NewStore[platformDoc](p2) // 无 WithPlatformLevel
	if err := s2.Create(bg, &platformDoc{ID: "menu-1", Name: "系统管理"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	n2, err := s2.Count(t1, nil)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("未豁免的实体 Count=%d, want 0（隔离生效：无 TenantID 字段故全被过滤）", n2)
	}
}

// TestStore_PlatformLevel_FalseByDefault 锁定 fail-closed：不声明即隔离生效。
func TestStore_PlatformLevel_FalseByDefault(t *testing.T) {
	p := inmemory.NewProvider[tenantDoc](func(d *tenantDoc) string { return d.ID })
	s := store.NewStore[tenantDoc](p)
	if s.IsPlatformLevel() {
		t.Fatal("默认必须为 false（fail-closed）：豁免须显式声明")
	}
}
