package inmemory

// inmemory_internal_test.go —— 包内钉子：覆盖外部测试触达不到的未导出助手。
// 外部用例（inmemory_test 包）只走 Store 公开面；操作符全集、反射边界、
// 排序/比较回退等分支在此逐条钉住，避免「删掉一个 operator 分支无测试报警」。

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

type rec struct {
	ID     string
	UserID string
	Name   string
	Age    int
	secret string
}

func valCond(field string, op storev1.Operator, value string) *storev1.FilterCondition {
	return &storev1.FilterCondition{
		Field:      field,
		Op:         op,
		ValueOneof: &storev1.FilterCondition_Value{Value: value},
	}
}

func valsCond(field string, op storev1.Operator, values ...string) *storev1.FilterCondition {
	return &storev1.FilterCondition{Field: field, Op: op, Values: values}
}

func TestNewProvider_Panics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewProvider(nil) should panic")
		}
	}()
	NewProvider[rec](nil)
}

func TestProviderAndQuery_Noops(t *testing.T) {
	p := NewProvider[rec](func(r *rec) string { return r.ID })
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := p.Migrate(context.Background(), &rec{}); err != nil {
		t.Fatalf("Provider.Migrate: %v", err)
	}
	q, err := p.DB(context.Background())
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if err := q.Migrate(context.Background()); err != nil {
		t.Fatalf("Queryable.Migrate: %v", err)
	}
}

func TestMatchWhere_Nil(t *testing.T) {
	if !matchWhere(&rec{}, nil) {
		t.Fatal("nil where should match everything")
	}
}

func TestCondValue_JSON(t *testing.T) {
	jv := structpb.NewStringValue("alice")
	c := &storev1.FilterCondition{ValueOneof: &storev1.FilterCondition_JsonValue{JsonValue: jv}}
	if got, want := condValue(c), jv.String(); got != want {
		t.Fatalf("condValue(json) = %q, want %q", got, want)
	}
	if got := condValue(valCond("Name", storev1.Operator_EQ, "bob")); got != "bob" {
		t.Fatalf("condValue(value) = %q, want bob", got)
	}
}

func TestMatchOne_Operators(t *testing.T) {
	obj := &rec{ID: "1", UserID: "u-1", Name: "Alice", Age: 30}
	cases := []struct {
		name string
		cond *storev1.FilterCondition
		want bool
	}{
		{"eq hit", valCond("Name", storev1.Operator_EQ, "Alice"), true},
		{"eq miss", valCond("Name", storev1.Operator_EQ, "alice"), false},
		{"exact", valCond("Name", storev1.Operator_EXACT, "Alice"), true},
		{"neq", valCond("Name", storev1.Operator_NEQ, "Bob"), true},
		{"in hit", valsCond("Name", storev1.Operator_IN, "Bob", "Alice"), true},
		{"in miss", valsCond("Name", storev1.Operator_IN, "Bob"), false},
		{"nin miss", valsCond("Name", storev1.Operator_NIN, "Alice"), false},
		{"nin hit", valsCond("Name", storev1.Operator_NIN, "Bob"), true},
		{"like", valCond("Name", storev1.Operator_LIKE, "lic"), true},
		{"contains", valCond("Name", storev1.Operator_CONTAINS, "Ali"), true},
		{"ilike", valCond("Name", storev1.Operator_ILIKE, "ALI"), true},
		{"icontains", valCond("Name", storev1.Operator_ICONTAINS, "LIC"), true},
		{"iexact", valCond("Name", storev1.Operator_IEXACT, "alice"), true},
		{"not like", valCond("Name", storev1.Operator_NOT_LIKE, "zzz"), true},
		{"starts with", valCond("Name", storev1.Operator_STARTS_WITH, "Al"), true},
		{"ends with", valCond("Name", storev1.Operator_ENDS_WITH, "ce"), true},
		{"istarts with", valCond("Name", storev1.Operator_ISTARTS_WITH, "al"), true},
		{"iends with", valCond("Name", storev1.Operator_IENDS_WITH, "CE"), true},
		{"gt", valCond("Age", storev1.Operator_GT, "29"), true},
		{"gt equal", valCond("Age", storev1.Operator_GT, "30"), false},
		{"gte", valCond("Age", storev1.Operator_GTE, "30"), true},
		{"lt", valCond("Age", storev1.Operator_LT, "31"), true},
		{"lte", valCond("Age", storev1.Operator_LTE, "30"), true},
		{"is null", valCond("secret", storev1.Operator_IS_NULL, ""), true},
		{"is not null", valCond("Name", storev1.Operator_IS_NOT_NULL, ""), true},
		{"is not null empty", valCond("secret", storev1.Operator_IS_NOT_NULL, ""), false},
		{"between", valsCond("Age", storev1.Operator_BETWEEN, "20", "40"), true},
		{"between out", valsCond("Age", storev1.Operator_BETWEEN, "31", "40"), false},
		{"between bad arity", valsCond("Age", storev1.Operator_BETWEEN, "20"), false},
		{"regexp", valCond("Name", storev1.Operator_REGEXP, "^Al"), true},
		{"iregexp", valCond("Name", storev1.Operator_IREGEXP, "^al"), true},
		{"regexp invalid", valCond("Name", storev1.Operator_REGEXP, "["), false},
		{"unsupported", valCond("Name", storev1.Operator_JSON_CONTAINS, "Alice"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchOne(obj, tc.cond); got != tc.want {
				t.Fatalf("matchOne(%v) = %v, want %v", tc.cond.GetOp(), got, tc.want)
			}
		})
	}

	// 空记录 + IS_NULL：nil 指针经 fieldString 归一为空串。
	if !matchOne((*rec)(nil), valCond("Name", storev1.Operator_IS_NULL, "")) {
		t.Fatal("nil record should read as null field")
	}
}

func TestFieldString_Edges(t *testing.T) {
	obj := &rec{ID: "1", UserID: "u-1", Name: "Alice", Age: 30, secret: "hidden"}
	if obj.secret != "hidden" {
		t.Fatalf("fixture secret = %q", obj.secret)
	}

	if got := fieldString(obj, "Age"); got != "30" {
		t.Fatalf("Age = %q, want 30", got)
	}
	// snake 归一：DTO 列名命中 Go 字段名。
	if got := fieldString(obj, "user_id"); got != "u-1" {
		t.Fatalf("user_id = %q, want u-1", got)
	}
	// 未导出字段不参与过滤。
	if got := fieldString(obj, "secret"); got != "" {
		t.Fatalf("unexported field = %q, want empty", got)
	}
	// 非结构体与 nil 指针一律空串。
	i := 5
	if got := fieldString(&i, "x"); got != "" {
		t.Fatalf("non-struct = %q, want empty", got)
	}
	if got := fieldString((*rec)(nil), "Name"); got != "" {
		t.Fatalf("nil pointer = %q, want empty", got)
	}
	if got := fieldString(obj, "Missing"); got != "" {
		t.Fatalf("missing field = %q, want empty", got)
	}
}

func TestSnake(t *testing.T) {
	cases := map[string]string{
		"ID":         "id",
		"UserID":     "user_id",
		"HTTPServer": "http_server",
		"lower":      "lower",
	}
	for in, want := range cases {
		if got := snake(in); got != want {
			t.Fatalf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLessBy(t *testing.T) {
	a := &rec{Name: "b", Age: 2}
	b := &rec{Name: "a", Age: 3}

	asc := []*storev1.Sorting{{Field: "Name", Direction: storev1.Sorting_ASC}}
	if lessBy(a, b, asc) {
		t.Fatal("ASC: b < a should be false")
	}
	if !lessBy(b, a, asc) {
		t.Fatal("ASC: a < b should be true")
	}

	desc := []*storev1.Sorting{{Field: "Name", Direction: storev1.Sorting_DESC}}
	if !lessBy(a, b, desc) {
		t.Fatal("DESC: b > a should be true")
	}

	// 首键相等回落次键。
	multi := []*storev1.Sorting{
		{Field: "Name", Direction: storev1.Sorting_ASC},
		{Field: "Age", Direction: storev1.Sorting_DESC},
	}
	if lessBy(&rec{Name: "x", Age: 2}, &rec{Name: "x", Age: 3}, multi) {
		t.Fatal("DESC 次键：Age 2 应排在 Age 3 之后")
	}
	if !lessBy(&rec{Name: "x", Age: 3}, &rec{Name: "x", Age: 2}, multi) {
		t.Fatal("multi-key tie should fall through to Age desc")
	}
	// 全键相等：不产生排序。
	if lessBy(&rec{Name: "x"}, &rec{Name: "x"}, asc) {
		t.Fatal("equal records should compare false")
	}
}

func TestCmpNum(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"9", "10", -1},
		{"10", "9", 1},
		{"1", "1", 0},
		{"abc", "abd", -1},
		{"b", "a", 1},
		{"same", "same", 0},
	}
	for _, tc := range cases {
		if got := cmpNum(tc.a, tc.b); got != tc.want {
			t.Fatalf("cmpNum(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
