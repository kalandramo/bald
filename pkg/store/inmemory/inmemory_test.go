package inmemory_test

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/kalandramo/bald/pkg/store/inmemory"
)

// user 是测试实体。
type user struct {
	ID   string
	Name string
	Age  int
}

func newStore() *store.Store[user] {
	p := inmemory.NewProvider[user](func(u *user) string { return u.ID })
	return store.NewStore[user](p)
}

func TestStore_CRUDAndPaging(t *testing.T) {
	ctx := context.Background()
	s := newStore()

	// Create + 冲突。
	if err := s.Create(ctx, &user{ID: "1", Name: "alice", Age: 30}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Create(ctx, &user{ID: "1", Name: "alice2", Age: 31}); err != store.ErrConflict {
		t.Fatalf("duplicate create should be ErrConflict, got %v", err)
	}

	// Get。
	got, err := s.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	if err != nil || got == nil || got.Name != "alice" {
		t.Fatalf("get: err=%v got=%+v", err, got)
	}

	// Update。
	if err := s.Update(ctx, &user{ID: "1", Name: "alice-x", Age: 32}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = s.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	if got.Name != "alice-x" || got.Age != 32 {
		t.Fatalf("update not applied: %+v", got)
	}

	// 多插入用于列表/分页/过滤。
	for i := 2; i <= 15; i++ {
		_ = s.Create(ctx, &user{ID: string(rune('0' + i)), Name: "u", Age: 20 + i})
	}

	// 过滤 + 排序 + 分页（第 1 页，每页 10）。
	req := &storev1.PagingRequest{
		Page:     storev1p(1),
		PageSize: storev1p(10),
		Sorting:  []*storev1.Sorting{store.SortDesc("age")},
		FilteringType: &storev1.PagingRequest_FilterExpr{FilterExpr: &storev1.FilterExpr{
			Type:       storev1.ExprType_AND,
			Conditions: []*storev1.FilterCondition{store.Gt("age", "25")},
		}},
	}
	res, err := s.ListWithPaging(ctx, req)
	if err != nil {
		t.Fatalf("list with paging: %v", err)
	}
	if res.Meta.GetTotal().GetValue() == 0 {
		t.Fatalf("expected total > 0, got %d", res.Meta.GetTotal().GetValue())
	}
	if len(res.Items) != 10 {
		t.Fatalf("expected 10 items on page 1, got %d", len(res.Items))
	}
	// 降序：第一条 age 应最大。
	if res.Items[0].Age < res.Items[1].Age {
		t.Fatalf("expected desc age order, got %d then %d", res.Items[0].Age, res.Items[1].Age)
	}

	// Delete。
	if err := s.Delete(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}}); err != store.ErrNotFound {
		t.Fatalf("after delete should be ErrNotFound, got %v", err)
	}
}

// storev1p 构造 *uint32（PagingRequest 的 page/page_size 为 proto3 optional，即 *uint32）。
func storev1p(v uint32) *uint32 { return &v }

// TestStore_OrExpr 锁定 OR 树执行语义：AND(Filters, Expr) 下 OR 分支正确组合。
func TestStore_OrExpr(t *testing.T) {
	ctx := context.Background()
	s := newStore()
	_ = s.Create(ctx, &user{ID: "1", Name: "alice", Age: 30})
	_ = s.Create(ctx, &user{ID: "2", Name: "bob", Age: 16})
	_ = s.Create(ctx, &user{ID: "3", Name: "carol", Age: 70})

	// alice OR carol（扁平 Filters 与 Expr 树 AND 连接：Filters 限定 age>18，树内 OR 命中两个名字）。
	where := &store.Where{
		Filters: []*storev1.FilterCondition{store.Gt("age", "18")},
		Expr: store.Or([]*storev1.FilterCondition{
			store.Eq("name", "alice"),
			store.Eq("name", "carol"),
		}),
	}
	got, total, err := s.List(ctx, where)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("expected 2 (alice OR carol, both >18), got %d", total)
	}

	// OR 组合嵌套组：(name=bob OR age>60) AND age>18 → 仅 carol。
	where = &store.Where{
		Expr: store.And(nil,
			store.Or([]*storev1.FilterCondition{store.Eq("name", "bob")}, store.Or([]*storev1.FilterCondition{store.Gt("age", "60")})),
			store.Or([]*storev1.FilterCondition{store.Gt("age", "18")}),
		),
	}
	got, total, err = s.List(ctx, where)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(got) != 1 || got[0].Name != "carol" {
		t.Fatalf("expected only carol, got %d items", total)
	}

	// 空 OR 节点恒假：一律查不到。
	where = &store.Where{Expr: store.Or(nil)}
	if _, total, _ = s.List(ctx, where); total != 0 {
		t.Fatalf("empty OR must match nothing, got %d", total)
	}

	// 空 AND 节点恒真。
	where = &store.Where{Expr: store.And(nil)}
	if _, total, _ = s.List(ctx, where); total != 3 {
		t.Fatalf("empty AND must match all, got %d", total)
	}
}
