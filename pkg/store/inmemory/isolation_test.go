package inmemory_test

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/kalandramo/bald/pkg/store/inmemory"
)

// tenantDoc 是带租户列的测试实体（隔离注入需实体有 tenant_id 字段/列）。
type tenantDoc struct {
	ID       string
	TenantID string
	Name     string
}

// TestStore_IsolationOnGetListCountDelete 锁定边界 1 收敛：Get/List/Count/Delete
// 也必须自动注入租户隔离（原缺陷：隔离仅覆盖 ListWithPaging，Get/List/Count/Delete
// 直接透传 Where，多租户应用若忘了 where.T(ctx) 会读到/删到全租户数据）。
//
// 触发条件与 ListWithPaging 一致：仅当 ctx 携带租户值且维度已注册时注入；非多租户
// 应用（未注册维度）零影响。
func TestStore_IsolationOnGetListCountDelete(t *testing.T) {
	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
	t.Cleanup(func() { store.UnregisterTenant("tenant_id") })

	p := inmemory.NewProvider[tenantDoc](func(d *tenantDoc) string { return d.ID })
	s := store.NewStore[tenantDoc](p)
	bg := context.Background()

	// 两个租户各一条记录（Create 时 ctx 无租户值 → injectWriteTenant 无操作，保留手设 TenantID）。
	if err := s.Create(bg, &tenantDoc{ID: "1", TenantID: "t-1", Name: "a"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Create(bg, &tenantDoc{ID: "2", TenantID: "t-2", Name: "b"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	t1 := contextx.WithTenantID(bg, "t-1")

	// Get：本租户命中；他租户记录被隔离挡住 → ErrNotFound。
	if _, err := s.Get(t1, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}}); err != nil {
		t.Fatalf("Get 本租户应命中: %v", err)
	}
	if _, err := s.Get(t1, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "2")}}); err != store.ErrNotFound {
		t.Fatalf("Get 他租户应被隔离 → ErrNotFound, got %v", err)
	}

	// Count：只数本租户（t-1 有 1 条，而非全表 2 条）。
	n, err := s.Count(t1, nil)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("Count 本租户=%d, want 1（隔离生效）", n)
	}

	// List：只列本租户。
	items, total, err := s.List(t1, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("List 本租户 total=%d len=%d, want 1/1", total, len(items))
	}
	if items[0].ID != "1" {
		t.Fatalf("List 返回他租户记录 %q（隔离失效）", items[0].ID)
	}

	// Delete：删他租户记录被隔离挡住 → rows=0（记录仍在）。
	rows, err := s.Delete(t1, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "2")}})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rows != 0 {
		t.Fatalf("Delete 他租户 rows=%d, want 0（隔离应挡住）", rows)
	}
	if _, err := s.Get(bg, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "2")}}); err != nil {
		t.Fatalf("他租户记录不应被删除: %v", err)
	}

	// Delete：删本租户 → rows=1。
	rows, err = s.Delete(t1, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	if err != nil {
		t.Fatalf("delete own: %v", err)
	}
	if rows != 1 {
		t.Fatalf("Delete 本租户 rows=%d, want 1", rows)
	}
}

// TestStore_Isolation_NoTenantCtxUnaffected 锁定：ctx 无租户值时（非多租户应用
// 或匿名请求），Get/List/Count/Delete 行为不变——隔离注入不产生副作用。
func TestStore_Isolation_NoTenantCtxUnaffected(t *testing.T) {
	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
	t.Cleanup(func() { store.UnregisterTenant("tenant_id") })

	p := inmemory.NewProvider[tenantDoc](func(d *tenantDoc) string { return d.ID })
	s := store.NewStore[tenantDoc](p)
	bg := context.Background()
	_ = s.Create(bg, &tenantDoc{ID: "1", TenantID: "t-1", Name: "a"})
	_ = s.Create(bg, &tenantDoc{ID: "2", TenantID: "t-2", Name: "b"})

	// 无租户 ctx：全表可见（2 条）。
	if n, _ := s.Count(bg, nil); n != 2 {
		t.Fatalf("无租户 ctx Count=%d, want 2（不应注入隔离）", n)
	}
	_, total, _ := s.List(bg, nil)
	if total != 2 {
		t.Fatalf("无租户 ctx List total=%d, want 2", total)
	}
}
