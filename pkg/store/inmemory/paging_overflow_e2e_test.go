package inmemory_test

import (
	"context"
	"errors"
	"math"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/store"
)

func u64pOverflow(v uint64) *uint64 { return &v }

// W-10：经真实 inmemory 后端走 ListWithPaging，超大 Offset 应返回 ErrInvalidToken，
// 而非静默回绕成负数导致后端行为不一致（translate → offsetPaginator.Resolve 即拦截）。
func TestStore_ListWithPaging_OffsetOverflowRejected(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	if err := s.Create(ctx, &user{ID: "1", Name: "alice"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err := s.ListWithPaging(ctx, &storev1.PagingRequest{
		Offset: u64pOverflow(uint64(math.MaxInt) + 1),
		Limit:  storev1p(10),
	})
	if !errors.Is(err, store.ErrInvalidToken) {
		t.Fatalf("ListWithPaging 超大 offset err=%v, want ErrInvalidToken", err)
	}
}

// 正常分页不回归：页码分页的第 2 页元数据正确。
func TestStore_ListWithPaging_NormalPagingUnchanged(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := s.Create(ctx, &user{ID: string(rune('a' + i)), Name: "u"}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	res, err := s.ListWithPaging(ctx, &storev1.PagingRequest{
		Page:     storev1p(2),
		PageSize: storev1p(2),
	})
	if err != nil {
		t.Fatalf("ListWithPaging: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items=%d, want 2", len(res.Items))
	}
	if got := res.Meta.GetTotal().GetValue(); got != 5 {
		t.Fatalf("total=%d, want 5", got)
	}
	if got := res.Meta.GetTotalPages().GetValue(); got != 3 {
		t.Fatalf("total_pages=%d, want 3", got)
	}
	if got := res.Meta.GetCurrentPage().GetValue(); got != 2 {
		t.Fatalf("current_page=%d, want 2", got)
	}
}
