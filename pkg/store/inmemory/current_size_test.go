package inmemory_test

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
)

// TestStore_ListWithPaging_CurrentSize 锁定 PaginationResponseMeta.CurrentSize 被填充
// （边界 6 修复）：它表示「当前页实际返回条数」，与 len(Items) 一致。
// 原缺陷：fillTotal 只回填 Total/TotalPages/NextToken，CurrentSize 恒为 nil（读出 0）。
func TestStore_ListWithPaging_CurrentSize(t *testing.T) {
	ctx := context.Background()
	s := newStore()
	for i := 0; i < 5; i++ {
		_ = s.Create(ctx, &user{ID: string(rune('a' + i)), Name: "u", Age: 20 + i})
	}

	// 第 1 页，每页 2 → 满页，CurrentSize=2。
	res, err := s.ListWithPaging(ctx, &storev1.PagingRequest{Page: storev1p(1), PageSize: storev1p(2)})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := res.Meta.GetCurrentSize(); got != 2 {
		t.Fatalf("page1 CurrentSize=%d, want 2", got)
	}
	if len(res.Items) != 2 {
		t.Fatalf("page1 items=%d, want 2", len(res.Items))
	}

	// 第 3 页（最后一页），每页 2 → 只剩 1 条，CurrentSize=1。
	res, err = s.ListWithPaging(ctx, &storev1.PagingRequest{Page: storev1p(3), PageSize: storev1p(2)})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := res.Meta.GetCurrentSize(); got != 1 {
		t.Fatalf("last page CurrentSize=%d, want 1", got)
	}

	// 超出范围的页 → 0 条，CurrentSize=0。
	res, err = s.ListWithPaging(ctx, &storev1.PagingRequest{Page: storev1p(99), PageSize: storev1p(2)})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := res.Meta.GetCurrentSize(); got != 0 {
		t.Fatalf("empty page CurrentSize=%d, want 0", got)
	}
	if len(res.Items) != 0 {
		t.Fatalf("empty page items=%d, want 0", len(res.Items))
	}
}
