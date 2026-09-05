package store

import storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"

// Where 是引擎无关的查询条件。
//
// 以 DTO 字段名表达（Where.Filters / Where.Sorting），由后端 Queryable 实现
// 翻译成各自的 SQL / NoSQL。核心 Store 不感知引擎方言，也不直接拼接查询。
type Where struct {
	// Offset 跳过记录数（从 0 开始）。
	Offset int
	// Limit 最多返回条数；<=0 表示不限制。
	Limit int
	// Filters 结构化过滤条件（来自 PagingRequest.FilterExpr）。
	Filters []*storev1.FilterCondition
	// Sorting 排序规则。
	Sorting []*storev1.Sorting
}

// Cond 构造一条等值过滤条件（便捷方法）。
func Cond(field string, op storev1.Operator, value string) *storev1.FilterCondition {
	return &storev1.FilterCondition{Field: field, Op: op, Value: value}
}

// Eq 构造一条等值（=）过滤条件。
func Eq(field, value string) *storev1.FilterCondition {
	return Cond(field, storev1.Operator_EQ, value)
}

// Ne 构造一条不等（!=）过滤条件。
func Ne(field, value string) *storev1.FilterCondition {
	return Cond(field, storev1.Operator_NEQ, value)
}

// Gt / Gte / Lt / Lte 构造比较条件。
func Gt(field, value string) *storev1.FilterCondition {
	return Cond(field, storev1.Operator_GT, value)
}
func Gte(field, value string) *storev1.FilterCondition {
	return Cond(field, storev1.Operator_GTE, value)
}
func Lt(field, value string) *storev1.FilterCondition {
	return Cond(field, storev1.Operator_LT, value)
}
func Lte(field, value string) *storev1.FilterCondition {
	return Cond(field, storev1.Operator_LTE, value)
}

// In / Nin 构造集合条件。
func In(field string, values ...string) *storev1.FilterCondition {
	return &storev1.FilterCondition{Field: field, Op: storev1.Operator_IN, Values: values}
}
func Nin(field string, values ...string) *storev1.FilterCondition {
	return &storev1.FilterCondition{Field: field, Op: storev1.Operator_NIN, Values: values}
}

// Like / Contains 构造模糊匹配条件。
func Like(field, value string) *storev1.FilterCondition {
	return Cond(field, storev1.Operator_LIKE, value)
}
func Contains(field, value string) *storev1.FilterCondition {
	return Cond(field, storev1.Operator_CONTAINS, value)
}

// Sort 构造一条升序排序规则。
func Sort(field string) *storev1.Sorting {
	return &storev1.Sorting{Field: field, Direction: storev1.Direction_ASC}
}

// SortDesc 构造一条降序排序规则。
func SortDesc(field string) *storev1.Sorting {
	return &storev1.Sorting{Field: field, Direction: storev1.Direction_DESC}
}
