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
	"sort"
	"strconv"
	"strings"
	"sync"

	storev1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/store/v1"
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

	// 排序。
	if len(where.Sorting) > 0 {
		sort.SliceStable(matched, func(i, j int) bool {
			return lessBy(matched[i], matched[j], where.Sorting)
		})
	}

	// 分页。
	if where.Offset > 0 && where.Offset < len(matched) {
		matched = matched[where.Offset:]
	} else if where.Offset >= len(matched) {
		matched = nil
	}
	if where.Limit > 0 && len(matched) > where.Limit {
		matched = matched[:where.Limit]
	}
	return matched, total, nil
}

func (q *memQuery[T]) Count(_ context.Context, where *store.Where) (int64, error) {
	q.p.mu.RLock()
	defer q.p.mu.RUnlock()
	return int64(len(q.matchAll(where))), nil
}

// matchAll 返回满足 where.Filters 的全部记录（按插入顺序）。
func (q *memQuery[T]) matchAll(where *store.Where) []*T {
	out := make([]*T, 0, len(q.p.data))
	var conds []*storev1.FilterCondition
	if where != nil {
		conds = where.Filters
	}
	for _, v := range q.p.data {
		if matchFilters(v, conds) {
			out = append(out, v)
		}
	}
	return out
}

// matchFilters 对单条记录评估全部过滤条件（AND 语义）。
func matchFilters[T any](obj *T, conds []*storev1.FilterCondition) bool {
	for _, c := range conds {
		if !matchOne(obj, c) {
			return false
		}
	}
	return true
}

func matchOne[T any](obj *T, c *storev1.FilterCondition) bool {
	got := fieldString(obj, c.GetField())
	switch c.GetOp() {
	case storev1.Operator_EQ:
		return got == c.GetValue()
	case storev1.Operator_NEQ:
		return got != c.GetValue()
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
		return strings.Contains(got, c.GetValue())
	case storev1.Operator_ILIKE:
		return strings.Contains(strings.ToLower(got), strings.ToLower(c.GetValue()))
	case storev1.Operator_STARTS_WITH:
		return strings.HasPrefix(got, c.GetValue())
	case storev1.Operator_ENDS_WITH:
		return strings.HasSuffix(got, c.GetValue())
	case storev1.Operator_GT:
		return cmpNum(got, c.GetValue()) > 0
	case storev1.Operator_GTE:
		return cmpNum(got, c.GetValue()) >= 0
	case storev1.Operator_LT:
		return cmpNum(got, c.GetValue()) < 0
	case storev1.Operator_LTE:
		return cmpNum(got, c.GetValue()) <= 0
	case storev1.Operator_IS_NULL:
		return got == ""
	case storev1.Operator_IS_NOT_NULL:
		return got != ""
	default:
		return false
	}
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
		if strings.EqualFold(f.Name, field) {
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
		if s.GetDirection() == storev1.Direction_DESC {
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
