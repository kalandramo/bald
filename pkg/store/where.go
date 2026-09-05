package store

import storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"

// Where 是引擎无关的查询条件。
//
// 以 DTO 字段名表达（Where.Filters / Where.Expr / Where.Sorting），由后端
// Queryable 实现翻译成各自的 SQL / NoSQL。核心 Store 不感知引擎方言，
// 也不直接拼接查询。
//
// 条件语义：WHERE = AND(Filters..., Expr)。
//   - Filters：扁平 AND 条件（框架内部注入租户隔离/数据范围也走这里，
//     与业务条件互不干扰）；
//   - Expr：完整布尔树（FilterExpr 支持 AND/OR 嵌套，承载复杂过滤与
//     DataScope 多范围 OR 组合）。空 OR 节点语义为恒假（布尔代数）。
type Where struct {
	// Offset 跳过记录数（从 0 开始）。
	Offset int
	// Limit 最多返回条数；<=0 表示不限制。
	Limit int
	// Filters 扁平 AND 过滤条件。
	Filters []*storev1.FilterCondition
	// Expr 复杂过滤表达式（AND/OR 树，可嵌套；与 Filters 按 AND 连接）。
	Expr *storev1.FilterExpr
	// Sorting 排序规则。
	Sorting []*storev1.Sorting
}

// Cond 构造一条等值过滤条件（便捷方法）。
func Cond(field string, op storev1.Operator, value string) *storev1.FilterCondition {
	return &storev1.FilterCondition{
		Field:      field,
		Op:         op,
		ValueOneof: &storev1.FilterCondition_Value{Value: value},
	}
}

// Eq 构造一条等值（=）过滤条件。
func Eq(field, value string) *storev1.FilterCondition {
	return Cond(field, storev1.Operator_EQ, value)
}

// Ne 构造一条不等于（!=）过滤条件。
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

// And 构造 AND 组合的表达式树（conditions 与 groups 求 AND）。
func And(conditions []*storev1.FilterCondition, groups ...*storev1.FilterExpr) *storev1.FilterExpr {
	return &storev1.FilterExpr{
		Type:       storev1.ExprType_AND,
		Conditions: conditions,
		Groups:     groups,
	}
}

// Or 构造 OR 组合的表达式树（conditions 与 groups 求 OR）。
// 空 OR 节点（零条件零子组）语义为恒假。
func Or(conditions []*storev1.FilterCondition, groups ...*storev1.FilterExpr) *storev1.FilterExpr {
	return &storev1.FilterExpr{
		Type:       storev1.ExprType_OR,
		Conditions: conditions,
		Groups:     groups,
	}
}

// Sort 构造一条升序排序规则。
func Sort(field string) *storev1.Sorting {
	return &storev1.Sorting{Field: field, Direction: storev1.Sorting_ASC}
}

// SortDesc 构造一条降序排序规则。
func SortDesc(field string) *storev1.Sorting {
	return &storev1.Sorting{Field: field, Direction: storev1.Sorting_DESC}
}
