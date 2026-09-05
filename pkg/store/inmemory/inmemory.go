// Package inmemory 提供 Store[T] 的内存实现（零外部依赖）。
//
// 用途：e2e 演示、单元测试、本地原型——无需任何数据库进程即可跑通
// 注册 → 增删改查 → 分页 全流程。生产请改用桥接的 GORM / MongoDB 后端。
//
// 记录以「键」索引；键由调用方提供的 KeyFunc 提取（典型为实体主键）。
// 过滤条件经反射读取导出字段值做字符串比较，覆盖 EQ/NEQ/IN/LIKE/CONTAINS
// /GT/LT 等常见操作符，足以驱动演示与测试。
package inmemory

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/store"
)

// KeyFunc 从实体提取索引键（通常为业务主键）。
type KeyFunc[T any] func(*T) string

// Provider 是内存版 DBProvider[T]。
type Provider[T any] struct {
	mu    sync.RWMutex
	data  map[string]*T
	keyOf KeyFunc[T]
}

// NewProvider 构造内存提供者。keyOf 不能为空（用于唯一索引实体）。
func NewProvider[T any](keyOf KeyFunc[T]) *Provider[T] {
	if keyOf == nil {
		panic("inmemory: KeyFunc must not be nil")
	}
	return &Provider[T]{data: make(map[string]*T), keyOf: keyOf}
}

// DB 返回内存 Queryable（所有操作在同一个 map 上）。
func (p *Provider[T]) DB(_ context.Context) (store.Queryable[T], error) {
	return &memQuery[T]{p: p}, nil
}

// Close 空实现（内存无连接）。
func (p *Provider[T]) Close() error { return nil }

// Migrate 空实现（内存无需建表）。
func (p *Provider[T]) Migrate(_ context.Context, _ ...any) error { return nil }

// Migrate 空实现（内存无需建表）。
func (q *memQuery[T]) Migrate(_ context.Context, _ ...any) error { return nil }

// memQuery 实现 store.Queryable[T]，操作 Provider 的 map。
type memQuery[T any] struct {
	p *Provider[T]
}

func (q *memQuery[T]) Create(_ context.Context, obj *T) error {
	q.p.mu.Lock()
	defer q.p.mu.Unlock()
	k := q.p.keyOf(obj)
	if _, ok := q.p.data[k]; ok {
		return store.ErrConflict
	}
	q.p.data[k] = obj
	return nil
}

func (q *memQuery[T]) Update(_ context.Context, obj *T) error {
	q.p.mu.Lock()
	defer q.p.mu.Unlock()
	k := q.p.keyOf(obj)
	if _, ok := q.p.data[k]; !ok {
		return store.ErrNotFound
	}
	q.p.data[k] = obj
	return nil
}

func (q *memQuery[T]) Delete(_ context.Context, where *store.Where) error {
	q.p.mu.Lock()
	defer q.p.mu.Unlock()
	matched := q.matchAll(where)
	if len(matched) == 0 {
		return store.ErrNotFound
	}
	for _, v := range matched {
		delete(q.p.data, q.p.keyOf(v))
	}
	return nil
}

func (q *memQuery[T]) Get(_ context.Context, where *store.Where) (*T, error) {
	q.p.mu.RLock()
	defer q.p.mu.RUnlock()
	matched := q.matchAll(where)
	for _, v := range matched {
		return v, nil
	}
	return nil, store.ErrNotFound
}

func (q *memQuery[T]) List(_ context.Context, where *store.Where) ([]*T, int64, error) {
	q.p.mu.RLock()
	defer q.p.mu.RUnlock()
	matched := q.matchAll(where)
	total := int64(len(matched))

	// where 可为 nil（无条件全列），先解引用保护。
	var offset, limit int
	var sorting []*storev1.Sorting
	if where != nil {
		offset, limit, sorting = where.Offset, where.Limit, where.Sorting
	}

	// 排序。
	if len(sorting) > 0 {
		sort.SliceStable(matched, func(i, j int) bool {
			return lessBy(matched[i], matched[j], sorting)
		})
	}

	// 分页。
	if offset > 0 && offset < len(matched) {
		matched = matched[offset:]
	} else if offset >= len(matched) && offset > 0 {
		matched = nil
	}
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, total, nil
}

func (q *memQuery[T]) Count(_ context.Context, where *store.Where) (int64, error) {
	q.p.mu.RLock()
	defer q.p.mu.RUnlock()
	return int64(len(q.matchAll(where))), nil
}

// matchAll 返回满足 where 条件的全部记录（按插入顺序）。
// 语义：AND(Filters..., Expr)，与 store.Where 的契约一致。
func (q *memQuery[T]) matchAll(where *store.Where) []*T {
	out := make([]*T, 0, len(q.p.data))
	var conds []*storev1.FilterCondition
	var expr *storev1.FilterExpr
	if where != nil {
		conds = where.Filters
		expr = where.Expr
	}
	for _, v := range q.p.data {
		if matchFilters(v, conds) && matchExpr(v, expr) {
			out = append(out, v)
		}
	}
	return out
}

// matchFilters 对单条记录评估全部扁平过滤条件（AND 语义）。
func matchFilters[T any](obj *T, conds []*storev1.FilterCondition) bool {
	for _, c := range conds {
		if !matchOne(obj, c) {
			return false
		}
	}
	return true
}

// matchExpr 递归评估布尔树：AND 节点全真才真，OR 节点任一真即真。
// 空 AND 节点恒真，空 OR 节点恒假（布尔代数，用于「禁止访问」语义）。
func matchExpr[T any](obj *T, fe *storev1.FilterExpr) bool {
	if fe == nil {
		return true
	}
	isOr := fe.GetType() == storev1.ExprType_OR
	if isOr && len(fe.GetConditions()) == 0 && len(fe.GetGroups()) == 0 {
		return false // 空 OR = 恒假
	}
	for _, c := range fe.GetConditions() {
		ok := matchOne(obj, c)
		if isOr && ok {
			return true
		}
		if !isOr && !ok {
			return false
		}
	}
	for _, g := range fe.GetGroups() {
		ok := matchExpr(obj, g)
		if isOr && ok {
			return true
		}
		if !isOr && !ok {
			return false
		}
	}
	return !isOr // AND 全部通过为真；OR 全部未命中为假
}

// condValue 返回条件比较值：优先 json_value（非字符串类型按 JSON 文本比较），
// 否则取字符串 value。
func condValue(c *storev1.FilterCondition) string {
	if jv := c.GetJsonValue(); jv != nil {
		return jv.String()
	}
	return c.GetValue()
}

func matchOne[T any](obj *T, c *storev1.FilterCondition) bool {
	got := fieldString(obj, c.GetField())
	want := condValue(c)
	switch c.GetOp() {
	case storev1.Operator_EQ, storev1.Operator_EXACT:
		return got == want
	case storev1.Operator_NEQ:
		return got != want
	case storev1.Operator_IN:
		for _, v := range c.GetValues() {
			if got == v {
				return true
			}
		}
		return false
	case storev1.Operator_NIN:
		for _, v := range c.GetValues() {
			if got == v {
				return false
			}
		}
		return true
	case storev1.Operator_LIKE, storev1.Operator_CONTAINS:
		return strings.Contains(got, want)
	case storev1.Operator_ILIKE, storev1.Operator_ICONTAINS:
		return strings.Contains(strings.ToLower(got), strings.ToLower(want))
	case storev1.Operator_IEXACT:
		return strings.EqualFold(got, want)
	case storev1.Operator_NOT_LIKE:
		return !strings.Contains(got, want)
	case storev1.Operator_STARTS_WITH:
		return strings.HasPrefix(got, want)
	case storev1.Operator_ENDS_WITH:
		return strings.HasSuffix(got, want)
	case storev1.Operator_ISTARTS_WITH:
		return strings.HasPrefix(strings.ToLower(got), strings.ToLower(want))
	case storev1.Operator_IENDS_WITH:
		return strings.HasSuffix(strings.ToLower(got), strings.ToLower(want))
	case storev1.Operator_GT:
		return cmpNum(got, want) > 0
	case storev1.Operator_GTE:
		return cmpNum(got, want) >= 0
	case storev1.Operator_LT:
		return cmpNum(got, want) < 0
	case storev1.Operator_LTE:
		return cmpNum(got, want) <= 0
	case storev1.Operator_IS_NULL:
		return got == ""
	case storev1.Operator_IS_NOT_NULL:
		return got != ""
	case storev1.Operator_BETWEEN:
		vals := c.GetValues()
		if len(vals) != 2 {
			return false
		}
		return cmpNum(got, vals[0]) >= 0 && cmpNum(got, vals[1]) <= 0
	case storev1.Operator_REGEXP, storev1.Operator_IREGEXP:
		pattern := want
		if c.GetOp() == storev1.Operator_IREGEXP {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false
		}
		return re.MatchString(got)
	// JSON_CONTAINS / ARRAY_CONTAINS / EXISTS / SEARCH 依赖引擎能力，
	// 内存实现不支持（default 返回 false，宁缺勿假）。
	default:
		return false
	}
}

// snake 把 Go 字段名归一为 snake_case（UserID→user_id），与 gorm 桥接的列名
// 约定一致，使同一 Where 语义跨后端表现相同。
func snake(name string) string {
	runes := []rune(name)
	var b strings.Builder
	for i, r := range runes {
		if r >= 'A' && r <= 'Z' {
			prevLower := i > 0 && runes[i-1] >= 'a' && runes[i-1] <= 'z'
			nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
			if i > 0 && (prevLower || (runes[i-1] >= 'A' && runes[i-1] <= 'Z' && nextLower)) {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// fieldString 经反射读取 obj 的导出字段值并格式化为字符串（大小写不敏感匹配字段名）。
func fieldString[T any](obj *T, field string) string {
	v := reflect.ValueOf(obj)
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return ""
	}
	field = strings.ToLower(field)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		// 双向归一：Go 字段名（UserID）与 DTO/列名（user_id）都能命中，
		// 保证同一 Where 语义在 inmemory 与 gorm 后端行为一致。
		if strings.EqualFold(snake(f.Name), field) {
			return fmt.Sprintf("%v", v.Field(i).Interface())
		}
	}
	return ""
}

// lessBy 按排序规则比较两条记录（多字段依次比较）。
func lessBy[T any](a, b *T, sorting []*storev1.Sorting) bool {
	for _, s := range sorting {
		av := fieldString(a, s.GetField())
		bv := fieldString(b, s.GetField())
		c := cmpNum(av, bv)
		if c == 0 {
			continue
		}
		if s.GetDirection() == storev1.Sorting_DESC {
			return c > 0
		}
		return c < 0
	}
	return false
}

// cmpNum 尝试数值比较；失败则按字典序。
func cmpNum(a, b string) int {
	af, aerr := strconv.ParseFloat(a, 64)
	bf, berr := strconv.ParseFloat(b, 64)
	if aerr == nil && berr == nil {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		default:
			return 0
		}
	}
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
