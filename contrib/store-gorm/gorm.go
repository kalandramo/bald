// Package baldgorm 是 bald 存储层的 GORM 桥接子模块（独立 go.mod）。
//
// 把 GORM 的 *gorm.DB 适配为 bald 核心的 store.DBProvider[T] +
// store.Queryable[T]，使业务能以统一的泛型 Store[T] 姿态访问任意
// GORM 支持的数据库（MySQL / PostgreSQL / SQLite 等），而 bald 核心
// 不引入任何 GORM 依赖（遵循 P5 零后端耦合）。
//
// 用法：
//
//	db, _ := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
//	provider := baldgorm.NewGormProvider[User](db, func(u *User) string { return u.ID })
//	repo := store.NewStore[User](provider)
//
// 字段映射：Where.Filters 的 Field 是 DTO/Entity 的 Go 字段名（或
// gorm.Column 名），经 toColumn 翻译成数据库列名（默认 snake_case）。
package baldgorm

import (
	"context"
	"reflect"
	"strings"

	"github.com/kalandramo/bald/pkg/store"
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"gorm.io/gorm"
)

// Provider 是 GORM 版 DBProvider[T]。
// 持有 *gorm.DB 与实体主键提取函数；DB() 返回绑定到具体表的 Queryable。
type Provider[T any] struct {
	db    *gorm.DB
	keyOf func(*T) string
}

// NewGormProvider 构造 GORM 提供者。
//   - db: 已打开的 *gorm.DB（建议业务先 AutoMigrate 或调用 provider.Migrate）。
//   - keyOf: 从实体提取主键，用于 Get/Update/Delete 的唯一定位（避免全表扫描）。
func NewGormProvider[T any](db *gorm.DB, keyOf func(*T) string) *Provider[T] {
	if db == nil {
		panic("baldgorm: db must not be nil")
	}
	if keyOf == nil {
		panic("baldgorm: keyOf must not be nil")
	}
	return &Provider[T]{db: db, keyOf: keyOf}
}

// DB 返回 GORM Queryable（会话绑定到 T 的表）。
func (p *Provider[T]) DB(_ context.Context) (store.Queryable[T], error) {
	return &gormQuery[T]{db: p.db, keyOf: p.keyOf}, nil
}

// Close 空实现（GORM 连接池由调用方管理生命周期）。
func (p *Provider[T]) Close() error { return nil }

// Migrate 在数据库侧 AutoMigrate 给定模型（默认用 T）。
func (p *Provider[T]) Migrate(_ context.Context, models ...any) error {
	if len(models) == 0 {
		var zero T
		models = []any{zero}
	}
	return p.db.AutoMigrate(models...)
}

// gormQuery 实现 store.Queryable[T]，把 Where 翻译成 GORM 链式调用。
type gormQuery[T any] struct {
	db    *gorm.DB
	keyOf func(*T) string
}

// Migrate 委托给会话级 AutoMigrate。
func (q *gormQuery[T]) Migrate(_ context.Context, models ...any) error {
	if len(models) == 0 {
		var zero T
		models = []any{zero}
	}
	return q.db.AutoMigrate(models...)
}

func (q *gormQuery[T]) Create(_ context.Context, obj *T) error {
	if err := q.db.Create(obj).Error; err != nil {
		if isUniqueViolation(err) {
			return store.ErrConflict
		}
		return err
	}
	return nil
}

func (q *gormQuery[T]) Update(_ context.Context, obj *T) error {
	k := q.keyOf(obj)
	// 用 map 形式更新：避免 GORM 对零值字段的"跳过"行为，并把主键列排除，
	// 防止主键被改写导致行错位。语义：主键不可变，零值字段也会被写入。
	res := q.db.Model(new(T)).Where(toColumn("id")+" = ?", k).Updates(toMapExcludeKey(obj))
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			return store.ErrConflict
		}
		return res.Error
	}
	if res.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// toMapExcludeKey 将对象反射为更新用的字段映射（列名→值），剔除主键列 id。
// 这样 Updates(map) 会写入所有导出字段（含零值），且不会误改主键。
func toMapExcludeKey(obj any) map[string]any {
	m := make(map[string]any)
	v := reflect.ValueOf(obj).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		col := toColumn(f.Name)
		if col == toColumn("id") {
			continue
		}
		m[col] = v.Field(i).Interface()
	}
	return m
}

func (q *gormQuery[T]) Delete(_ context.Context, where *store.Where) error {
	tx := q.applyWhere(where)
	res := tx.Delete(new(T))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (q *gormQuery[T]) Get(_ context.Context, where *store.Where) (*T, error) {
	var obj T
	tx := q.applyWhere(where).Limit(1)
	if err := tx.First(&obj).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &obj, nil
}

func (q *gormQuery[T]) List(_ context.Context, where *store.Where) ([]*T, int64, error) {
	var total int64
	if err := q.applyWhere(where).Model(new(T)).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	tx := q.applyWhere(where)
	tx = q.applySorting(tx, where.Sorting)
	tx = q.applyPaging(tx, where)
	var items []*T
	if err := tx.Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (q *gormQuery[T]) Count(_ context.Context, where *store.Where) (int64, error) {
	var n int64
	if err := q.applyWhere(where).Model(new(T)).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// applyWhere 把 Where 的所有过滤条件翻译为 GORM 查询作用域。
func (q *gormQuery[T]) applyWhere(where *store.Where) *gorm.DB {
	tx := q.db
	if where == nil {
		return tx
	}
	for _, c := range where.Filters {
		tx = q.applyFilter(tx, c)
	}
	return tx
}

// applyFilter 翻译单条过滤条件。
func (q *gormQuery[T]) applyFilter(tx *gorm.DB, c *storev1.FilterCondition) *gorm.DB {
	col := toColumn(c.GetField())
	switch c.GetOp() {
	case storev1.Operator_EQ:
		return tx.Where(col+" = ?", c.GetValue())
	case storev1.Operator_NEQ:
		return tx.Where(col+" <> ?", c.GetValue())
	case storev1.Operator_GT:
		return tx.Where(col+" > ?", c.GetValue())
	case storev1.Operator_GTE:
		return tx.Where(col+" >= ?", c.GetValue())
	case storev1.Operator_LT:
		return tx.Where(col+" < ?", c.GetValue())
	case storev1.Operator_LTE:
		return tx.Where(col+" <= ?", c.GetValue())
	case storev1.Operator_LIKE:
		return tx.Where(col+" LIKE ?", c.GetValue())
	case storev1.Operator_ILIKE:
		// 仅对字符串类型列安全；非字符串列的 ILIKE 由调用方自行保证。
		// 优先用数据库原生不区分大小写匹配（MySQL 8 / PG 支持），
		// 退化为 lower() 仅作 SQLite 兼容兜底。
		return tx.Where("LOWER("+col+") LIKE LOWER(?)", c.GetValue())
	case storev1.Operator_NOT_LIKE:
		return tx.Where(col+" NOT LIKE ?", c.GetValue())
	case storev1.Operator_IN:
		return tx.Where(col+" IN ?", c.GetValues())
	case storev1.Operator_NIN:
		return tx.Where(col+" NOT IN ?", c.GetValues())
	case storev1.Operator_IS_NULL:
		return tx.Where(col + " IS NULL")
	case storev1.Operator_IS_NOT_NULL:
		return tx.Where(col + " IS NOT NULL")
	case storev1.Operator_CONTAINS:
		return tx.Where(col+" LIKE ?", "%"+c.GetValue()+"%")
	case storev1.Operator_STARTS_WITH:
		return tx.Where(col+" LIKE ?", c.GetValue()+"%")
	case storev1.Operator_ENDS_WITH:
		return tx.Where(col+" LIKE ?", "%"+c.GetValue())
	case storev1.Operator_BETWEEN:
		vals := c.GetValues()
		if len(vals) == 2 {
			return tx.Where(col+" BETWEEN ? AND ?", vals[0], vals[1])
		}
		return tx
	default:
		// 未知操作符：安全忽略（不拼非法 SQL）。
		return tx
	}
}

// applySorting 翻译排序规则。
func (q *gormQuery[T]) applySorting(tx *gorm.DB, sorting []*storev1.Sorting) *gorm.DB {
	for _, s := range sorting {
		col := toColumn(s.GetField())
		dir := "ASC"
		if s.GetDirection() == storev1.Direction_DESC {
			dir = "DESC"
		}
		tx = tx.Order(col + " " + dir)
	}
	return tx
}

// applyPaging 翻译偏移/限制分页。
func (q *gormQuery[T]) applyPaging(tx *gorm.DB, where *store.Where) *gorm.DB {
	if where.Offset > 0 {
		tx = tx.Offset(where.Offset)
	}
	if where.Limit > 0 {
		tx = tx.Limit(where.Limit)
	}
	return tx
}

// toColumn 把 Go 字段名翻译为数据库列名（默认 snake_case）。
// 业务可用 gorm.Column tag 覆盖；本桥接不读 tag，仅做命名约定转换，
// 足够演示与常见约定；如需精确列名，Where.Filter 直接传列名即可。
func toColumn(field string) string {
	if field == "" {
		return field
	}
	runes := []rune(field)
	var b strings.Builder
	for i, r := range runes {
		if r >= 'A' && r <= 'Z' {
			// 仅在「前一个是小写」或「前一个大写但后一个小写（缩写结尾）」时插入下划线，
			// 以正确处理缩写：ID→id、UserID→user_id、HTTPServer→http_server。
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

// isUniqueViolation 识别 GORM 返回的唯一约束冲突错误（跨驱动尽力而为）。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") ||
		strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "constraint")
}
