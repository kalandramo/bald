package mech

import (
	"testing"

	"github.com/kalandramo/bald/bootstrap/config"
)

// TestDiffStrings 差集工具契约（B5 自 appkit 迁入，实现体在本包）：有序、
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

// TestParseStringListForward 薄转发钉住：mech.ParseStringList 与
// config.StringList 同实现（实现体已随批 3 职责边界落点迁至
// bootstrap/config——最终归宿，见 util.go 落点说明）。
func TestParseStringListForward(t *testing.T) {
	input := []any{"a", 1, "b"}
	if got, want := ParseStringList(input), config.StringList(input); len(got) != len(want) || len(got) != 3 || got[1] != "1" {
		t.Errorf("forward mismatch: mech=%v config=%v (want converged semantics [a 1 b])", got, want)
	}
	if got := ParseStringList(""); got != nil {
		t.Errorf("empty = %v, want nil", got)
	}
}
