package store

import (
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
)

// TestWhereConstructors：条件便捷构造家族（R4 复审——谓词族对称性契约）。
// 零调用成员 Lt/Lte/Nin 与在用成员（Eq/Ne/Gt/Gte/In/Like）以同一断言钉住
// 形状，防翻译层（gorm applyFilter 等）语义漂移时无测试可依。
func TestWhereConstructors(t *testing.T) {
	tests := []struct {
		name    string
		got     *storev1.FilterCondition
		wantOp  storev1.Operator
		wantVal string
	}{
		{name: "Eq 等值", got: Eq("f", "v"), wantOp: storev1.Operator_EQ, wantVal: "v"},
		{name: "Ne 不等", got: Ne("f", "v"), wantOp: storev1.Operator_NEQ, wantVal: "v"},
		{name: "Gt 大于", got: Gt("f", "5"), wantOp: storev1.Operator_GT, wantVal: "5"},
		{name: "Gte 大于等于", got: Gte("f", "5"), wantOp: storev1.Operator_GTE, wantVal: "5"},
		{name: "Lt 小于（R4 零调用补测）", got: Lt("f", "5"), wantOp: storev1.Operator_LT, wantVal: "5"},
		{name: "Lte 小于等于（R4 零调用补测）", got: Lte("f", "5"), wantOp: storev1.Operator_LTE, wantVal: "5"},
		{name: "Like 模糊", got: Like("f", "%v%"), wantOp: storev1.Operator_LIKE, wantVal: "%v%"},
		{name: "Contains 包含", got: Contains("f", "v"), wantOp: storev1.Operator_CONTAINS, wantVal: "v"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got.GetField() != "f" {
				t.Errorf("Field = %q, want %q", tt.got.GetField(), "f")
			}
			if tt.got.GetOp() != tt.wantOp {
				t.Errorf("Op = %v, want %v", tt.got.GetOp(), tt.wantOp)
			}
			if tt.got.GetValue() != tt.wantVal {
				t.Errorf("Value = %q, want %q", tt.got.GetValue(), tt.wantVal)
			}
		})
	}

	// Cond 通用构造：与 Eq 等价。
	if c := Cond("f", storev1.Operator_EQ, "v"); c.GetOp() != storev1.Operator_EQ || c.GetValue() != "v" {
		t.Errorf("Cond 通用构造形状错误: %+v", c)
	}
}

// TestWhereSetConstructors：In/Nin 集合条件（R4 零调用成员 Nin 补测）。
func TestWhereSetConstructors(t *testing.T) {
	in := In("f", "a", "b")
	if in.GetOp() != storev1.Operator_IN {
		t.Errorf("In Op = %v, want IN", in.GetOp())
	}
	if vals := in.GetValues(); len(vals) != 2 || vals[0] != "a" || vals[1] != "b" {
		t.Errorf("In Values = %v, want [a b]", vals)
	}

	nin := Nin("f", "x")
	if nin.GetOp() != storev1.Operator_NIN {
		t.Errorf("Nin Op = %v, want NIN", nin.GetOp())
	}
	if vals := nin.GetValues(); len(vals) != 1 || vals[0] != "x" {
		t.Errorf("Nin Values = %v, want [x]", vals)
	}

	// 空集合：形状仍合法（翻译层语义负责）。
	if empty := In("f"); len(empty.GetValues()) != 0 {
		t.Errorf("In 空集合 Values 应为空, got %v", empty.GetValues())
	}
}

// TestWhereExprConstructors：And/Or 表达式树与排序方向。
func TestWhereExprConstructors(t *testing.T) {
	conds := []*storev1.FilterCondition{Eq("a", "1"), Ne("b", "2")}
	sub := Or(nil)

	and := And(conds, sub)
	if and.GetType() != storev1.ExprType_AND {
		t.Errorf("And Type = %v, want AND", and.GetType())
	}
	if len(and.GetConditions()) != 2 || len(and.GetGroups()) != 1 {
		t.Errorf("And 应携带 2 条件 + 1 子组, got %+v", and)
	}

	or := Or(conds, and)
	if or.GetType() != storev1.ExprType_OR {
		t.Errorf("Or Type = %v, want OR", or.GetType())
	}

	if s := Sort("created_at"); s.GetDirection() != storev1.Sorting_ASC {
		t.Errorf("Sort 方向 = %v, want ASC", s.GetDirection())
	}
	if s := SortDesc("created_at"); s.GetDirection() != storev1.Sorting_DESC {
		t.Errorf("SortDesc 方向 = %v, want DESC", s.GetDirection())
	}
}
