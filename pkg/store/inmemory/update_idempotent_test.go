package inmemory_test

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/store"
)

// TestStore_UpdateIdempotent 锁定 Update 的**幂等契约**（与 Delete 对称）。
//
// 背景：Update 原先对「目标记录不存在」返回 ErrNotFound，与 SQL 原生语义
// 冲突——更新 0 行是正常执行，ORM 不擅自增加底层没有的错误。现改为返回受影响
// 行数：0 行 = (0, nil)。更新的目标是让数据变成目标值，本来就是目标值，
// 操作本身成功。
//
// 需要「必须存在才能更新」的调用方，判 rows == 0 自行实现（无需额外 Get 往返）。
func TestStore_UpdateIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newStore()
	_ = s.Create(ctx, &user{ID: "1", Name: "alice", Age: 30})

	// 1) 更新已存在 → rows == 1，无错。
	rows, err := s.Update(ctx, &user{ID: "1", Name: "alice-x", Age: 32})
	if err != nil {
		t.Fatalf("update existing: err=%v, want nil", err)
	}
	if rows != 1 {
		t.Fatalf("update existing: rows=%d, want 1", rows)
	}
	got, _ := s.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	if got.Name != "alice-x" || got.Age != 32 {
		t.Fatalf("update not applied: %+v", got)
	}

	// 2) **核心**：更新不存在 → (0, nil)，不报 ErrNotFound（幂等）。
	rows, err = s.Update(ctx, &user{ID: "no-such", Name: "ghost", Age: 1})
	if err != nil {
		t.Fatalf("update missing must be idempotent: err=%v, want nil (幂等语义)", err)
	}
	if rows != 0 {
		t.Fatalf("update missing: rows=%d, want 0", rows)
	}

	// 3) 更新到"本就是目标值" → 仍是成功执行（面向最终状态，非过程）。
	rows, err = s.Update(ctx, &user{ID: "1", Name: "alice-x", Age: 32})
	if err != nil {
		t.Fatalf("update to same value: err=%v, want nil", err)
	}
	if rows != 1 {
		t.Fatalf("update to same value: rows=%d, want 1", rows)
	}

	// 4) 「必须存在才能更新」的上层显式实现：判 rows == 0。
	rows, _ = s.Update(ctx, &user{ID: "gone", Name: "x", Age: 1})
	if rows == 0 {
		t.Logf("上层可显式判 rows==0 实现'必须存在'语义 ✅")
	} else {
		t.Fatalf("rows=%d, want 0 for missing row", rows)
	}
}
