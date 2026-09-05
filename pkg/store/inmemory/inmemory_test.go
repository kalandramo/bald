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
		FilterExpr: &storev1.FilterExpr{
			Type:       storev1.FilterExpr_AND,
			Conditions: []*storev1.FilterCondition{store.Gt("age", "25")},
		},
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
