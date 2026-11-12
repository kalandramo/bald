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

// tenantDoc 是带租户列的测试实体（隔离注入需实体有 tenant_id 字段/列）。
type tenantDoc struct {
	ID       string
	TenantID string
	Name     string
}

// TestStore_IsolationOnGetListCountDelete 锁定：Get/List/Count/Delete 也必须
// 自动注入租户隔离（若隔离仅覆盖 ListWithPaging，Get/List/Count/Delete 直接
// 透传 Where，多租户应用忘了 where.T(ctx) 会读到/删到全租户数据）。
//
// 触发条件与 ListWithPaging 一致：仅当 ctx 携带租户值且维度已注册时注入；非多租户
// 应用（未注册维度）零影响。
func TestStore_IsolationOnGetListCountDelete(t *testing.T) {
	p := inmemory.NewProvider[tenantDoc](func(d *tenantDoc) string { return d.ID })
	s := store.NewStore[tenantDoc](p)
	bg := context.Background()

	// 先播种数据：此时尚未注册租户维度，Create 不做租户注入
	// （注册维度后 Create 要求 ctx 带租户，不能再用裸 ctx 造种子数据）。
	if err := s.Create(bg, &tenantDoc{ID: "1", TenantID: "t-1", Name: "a"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Create(bg, &tenantDoc{ID: "2", TenantID: "t-2", Name: "b"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 再开启多租户隔离（业务真实姿势：启动期注册维度）。
	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
	t.Cleanup(func() { store.UnregisterTenant("tenant_id") })

	t1 := contextx.WithTenantID(bg, "t-1")
	t2 := contextx.WithTenantID(bg, "t-2")

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
	if _, err := s.Get(t2, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "2")}}); err != nil {
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

// TestStore_UpdateIsolation 锁定：Update 也自动注入租户隔离——更新他租户记录
// 返回 (0, nil) 且数据不被改动（否则越权者可凭主键改写他租户数据并把归属
// 迁到自己租户）。
func TestStore_UpdateIsolation(t *testing.T) {
	p := inmemory.NewProvider[tenantDoc](func(d *tenantDoc) string { return d.ID })
	s := store.NewStore[tenantDoc](p)
	bg := context.Background()
	// 先播种（未注册维度时 Create 不做租户注入），再开启隔离。
	if err := s.Create(bg, &tenantDoc{ID: "1", TenantID: "t-1", Name: "a"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Create(bg, &tenantDoc{ID: "2", TenantID: "t-2", Name: "b"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
	t.Cleanup(func() { store.UnregisterTenant("tenant_id") })

	t1 := contextx.WithTenantID(bg, "t-1")
	t2 := contextx.WithTenantID(bg, "t-2")

	// 跨租户更新：id=2 属于 t-2，t-1 的身份不得改写它。
	rows, err := s.Update(t1, &tenantDoc{ID: "2", Name: "hijacked"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if rows != 0 {
		t.Fatalf("跨租户 Update rows=%d, want 0（隔离应挡住）", rows)
	}
	got, err := s.Get(t2, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "2")}})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "b" || got.TenantID != "t-2" {
		t.Fatalf("他租户记录被改写: %+v", got)
	}

	// 本租户更新：命中 1 行，租户归属保持在 t-1。
	rows, err = s.Update(t1, &tenantDoc{ID: "1", Name: "a2"})
	if err != nil {
		t.Fatalf("update own: %v", err)
	}
	if rows != 1 {
		t.Fatalf("本租户 Update rows=%d, want 1", rows)
	}
	got, err = s.Get(t1, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	if err != nil {
		t.Fatalf("get own: %v", err)
	}
	if got.Name != "a2" || got.TenantID != "t-1" {
		t.Fatalf("本租户记录更新异常: %+v", got)
	}
}

// TestStore_Isolation_NoTenantCtxRejected 锁定：多租户应用里 ctx 无租户值时，
// 读写路径**必须拒绝**（ErrMissingTenant），不得静默返回全表。
//
// ⚠️ 本测试是**安全回归**，原版本名为 `NoTenantCtxUnaffected`，断言
// 「无租户 ctx Count==2（全表可见）」——它把 fail-open 钉成了期望值，
// 让这个跨租户泄漏洞在 CI 里长期绿灯。同批修正：
//   - 现在断言 Count/List/Get/Delete 全部返回 ErrMissingTenant；
//   - 并额外断言「非多租户应用（未注册维度）」仍然不报错、全表可见——
//     把"身份不完整"和"这不是多租户应用"两件事区分开。
func TestStore_Isolation_NoTenantCtxRejected(t *testing.T) {
	p := inmemory.NewProvider[tenantDoc](func(d *tenantDoc) string { return d.ID })
	s := store.NewStore[tenantDoc](p)
	bg := context.Background()
	// 先播种，再开启隔离。
	if err := s.Create(bg, &tenantDoc{ID: "1", TenantID: "t-1", Name: "a"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Create(bg, &tenantDoc{ID: "2", TenantID: "t-2", Name: "b"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 非多租户应用：未注册维度 ⇒ 无隔离、不报错、全表可见（合法）。
	if n, err := s.Count(bg, nil); err != nil || n != 2 {
		t.Fatalf("非多租户应用 Count=%d err=%v, want 2/nil", n, err)
	}

	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
	t.Cleanup(func() { store.UnregisterTenant("tenant_id") })

	// 多租户应用 + 身份缺租户 ⇒ 一律拒绝（J4：身份不完整 fail-closed）。
	if n, err := s.Count(bg, nil); !errors.Is(err, store.ErrMissingTenant) {
		t.Fatalf("无租户 ctx Count=%d err=%v, want ErrMissingTenant", n, err)
	}
	if _, _, err := s.List(bg, nil); !errors.Is(err, store.ErrMissingTenant) {
		t.Fatalf("无租户 ctx List err=%v, want ErrMissingTenant", err)
	}
	if _, err := s.Get(bg, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}}); !errors.Is(err, store.ErrMissingTenant) {
		t.Fatalf("无租户 ctx Get err=%v, want ErrMissingTenant", err)
	}
	if _, err := s.Delete(bg, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}}); !errors.Is(err, store.ErrMissingTenant) {
		t.Fatalf("无租户 ctx Delete err=%v, want ErrMissingTenant", err)
	}
}
