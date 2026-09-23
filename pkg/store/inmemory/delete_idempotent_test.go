package inmemory_test

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/kalandramo/bald/pkg/store/inmemory"
)

// TestStore_DeleteIdempotent 锁定 Delete 的**幂等契约**（2026-09-23 决策）。
//
// 背景：Delete 原先对 0 行匹配返回 ErrNotFound，混用「单条删除的存在性断言」
// 与「集合删除的批量执行」两种语义（框架缺陷报告 D12）。现改为返回受影响
// 行数：0 行 = (0, nil)，删除不存在的资源是合法结果。
//
// 需要「必须存在才能删」的调用方，判 rows == 0 自行实现（无需额外 Get 往返）。
func TestStore_DeleteIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newStore()
	_ = s.Create(ctx, &user{ID: "1", Name: "alice", Age: 30})

	// 1) 删已存在 → rows == 1，无错。
	rows, err := s.Delete(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	if err != nil {
		t.Fatalf("delete existing: err=%v, want nil", err)
	}
	if rows != 1 {
		t.Fatalf("delete existing: rows=%d, want 1", rows)
	}

	// 2) **核心**：删不存在 → (0, nil)，不报 ErrNotFound（幂等）。
	rows, err = s.Delete(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "no-such")}})
	if err != nil {
		t.Fatalf("delete missing must be idempotent: err=%v, want nil (幂等语义)", err)
	}
	if rows != 0 {
		t.Fatalf("delete missing: rows=%d, want 0", rows)
	}

	// 3) 集合删除命中多行 → rows == 命中数（集合语义合法，不再误报 not found）。
	for _, u := range []*user{{ID: "2", Name: "b"}, {ID: "3", Name: "c"}, {ID: "4", Name: "d"}} {
		_ = s.Create(ctx, u)
	}
	rows, err = s.Delete(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.In("id", "2", "3", "4")}})
	if err != nil {
		t.Fatalf("delete set: err=%v, want nil", err)
	}
	if rows != 3 {
		t.Fatalf("delete set: rows=%d, want 3", rows)
	}

	// 4) 空集合条件删除 → (0, nil)，同样是合法结果（D12 原始误报场景）。
	rows, err = s.Delete(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("name", "nobody")}})
	if err != nil {
		t.Fatalf("delete empty set must be idempotent: err=%v, want nil", err)
	}
	if rows != 0 {
		t.Fatalf("delete empty set: rows=%d, want 0", rows)
	}

	// 5) 「必须存在才能删」的上层显式实现：判 rows == 0。
	rows, _ = s.Delete(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "gone")}})
	if rows == 0 {
		// 上层可据此返回 NotFound —— 证明能力面完整（零额外 Get 往返）。
		t.Logf("上层可显式判 rows==0 实现'必须存在'语义 ✅")
	} else {
		t.Fatalf("rows=%d, want 0 for missing row", rows)
	}
}

var _ = inmemory.NewProvider[user]
