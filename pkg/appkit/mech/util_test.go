package mech

import (
	"testing"
)

// TestDiffStrings 差集工具契约（实现体在本包）：有序、
// 去重、无交集。
func TestDiffStrings(t *testing.T) {
	add, remove := DiffStrings([]string{"a", "b"}, []string{"b", "c"})
	if len(add) != 1 || add[0] != "a" {
		t.Errorf("add = %v, want [a]", add)
	}
	if len(remove) != 1 || remove[0] != "c" {
		t.Errorf("remove = %v, want [c]", remove)
	}
	// 完全一致 → 空差集（收敛终态）。
	add, remove = DiffStrings([]string{"a"}, []string{"a"})
	if len(add) != 0 || len(remove) != 0 {
		t.Errorf("converged diff = (%v,%v), want empty", add, remove)
	}
}
