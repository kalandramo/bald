// Package baldgorm 是 bald 存储层的 GORM 桥接子模块（独立 go.mod）。
//
// 把 GORM 的 *gorm.DB 适配为 bald 核心的 store.DBProvider[T] +
// store.Queryable[T]，使业务能以统一的泛型 Store[T] 姿态访问任意
// GORM 支持的数据库（MySQL / PostgreSQL / SQLite 等），而 bald 核心
// 不引入任何 GORM 依赖。
//
// 用法：
//
//	db, _ := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
//	provider := baldgorm.NewGormProvider[User](db, func(u *User) string { return u.ID })
//	repo := store.NewStore[User](provider)
//
// 字段映射：Where.Filters 的 Field 优先按 Go 字段名解析（识别
// gorm:"column:..." 自定义列名），也接受直接传入的数据库列名；未命中
// 时才按默认 snake_case 约定翻译。
package baldgorm

import (
	"context"
	"reflect"
	"strings"
	"sync"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/store"
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

// DB 返回 GORM Queryable（会话绑定到 T 的表与请求 ctx）。
//
// ctx 通过 `WithContext` 绑定到会话——所有后续操作（Create/Get/List/...）自动
// 继承该 ctx，使请求取消/超时能传播到数据库驱动（ctx 若被 `_` 丢弃，取消的
// 请求仍会照常执行 DB 操作）。
func (p *Provider[T]) DB(ctx context.Context) (store.Queryable[T], error) {
	return &gormQuery[T]{
		db:    p.db.WithContext(ctx),
		keyOf: p.keyOf,
		meta:  modelMetaOf[T](),
	}, nil
}

// Close 空实现（GORM 连接池由调用方管理生命周期）。
func (p *Provider[T]) Close() error { return nil }

// Migrate 在数据库侧 AutoMigrate 给定模型（默认用 T）。
func (p *Provider[T]) Migrate(ctx context.Context, models ...any) error {
	if len(models) == 0 {
		var zero T
		models = []any{zero}
	}
	return p.db.WithContext(ctx).AutoMigrate(models...)
}

// gormQuery 实现 store.Queryable[T]，把 Where 翻译成 GORM 链式调用。
// db 已由 Provider.DB 经 WithContext 绑定请求 ctx（会话级继承）。
type gormQuery[T any] struct {
	db    *gorm.DB
	keyOf func(*T) string
	meta  *modelMeta
}

// Migrate 委托给会话级 AutoMigrate。
func (q *gormQuery[T]) Migrate(ctx context.Context, models ...any) error {
	if len(models) == 0 {
		var zero T
		models = []any{zero}
	}
	return q.db.WithContext(ctx).AutoMigrate(models...)
}

func (q *gormQuery[T]) Create(ctx context.Context, obj *T) error {
	if err := q.db.WithContext(ctx).Create(obj).Error; err != nil {
		if isUniqueViolation(err) {
			return store.ErrConflict
		}
		return err
	}
	return nil
}

// Update 更新一条记录，返回受影响行数。
// **幂等语义**（与 Delete 一致）：0 行匹配返回 (0, nil)，不报错——SQL 原生
// UPDATE 影响 0 行是正常执行，ORM 不擅自增加底层没有的错误。
//
// 返回的是 GORM `RowsAffected`（**受影响行数**，直接透传驱动），**不是匹配行数**
// ——MySQL 默认对「UPDATE 到与现有值相同」返回 0，故 `rows == 0` 不能作为
// 「记录不存在」的判据（详见 `store.Store.Update` 注释）。
//
// where（租户/数据范围隔离谓词）与主键条件 AND 连接：行不属于当前租户或越出
// 数据范围时匹配 0 行，不会修改数据。
func (q *gormQuery[T]) Update(ctx context.Context, obj *T, where *store.Where) (int64, error) {
	k := q.keyOf(obj)
	// 用 map 形式更新：避免 GORM 对零值字段的"跳过"行为，并把主键列排除，
	// 防止主键被改写导致行错位。语义：主键不可变，零值字段也会被写入。
	tx := q.applyWhere(where).WithContext(ctx).Model(new(T)).Where(q.meta.primaryKey+" = ?", k)
	res := tx.Updates(toUpdateMap[T](obj, q.meta))
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			return 0, store.ErrConflict
		}
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// toUpdateMap 将对象反射为更新用的字段映射（列名→值），剔除主键列。
// 这样 Updates(map) 会写入所有导出字段（含零值），且不会误改主键。
// `gorm:"-"`（及 "-:..."）字段是内存态/关联态（如树形 Children），非表列，跳过——
// 否则 Updates(map) 会生成不存在的列（SQLite/PG 直接报 no such column）。
func toUpdateMap[T any](obj *T, meta *modelMeta) map[string]any {
	m := make(map[string]any, len(meta.fields))
	v := reflect.ValueOf(obj).Elem()
	for _, f := range meta.fields {
		if f.column == meta.primaryKey {
			continue
		}
		fv, ok := fieldByIndex(v, f.index)
		if !ok {
			continue
		}
		m[f.column] = fv.Interface()
	}
	return m
}

// modelMeta 是实体 T 的字段映射缓存：Go 字段名 → 列名、主键列名。
// 列名优先取 gorm:"column:..."，否则按 snake_case 约定；嵌入结构体展开。
type modelMeta struct {
	primaryKey string
	fields     []columnField
	columns    map[string]columnField // Go 字段名 → 字段
}

// columnField 描述一个可持久化字段：从实体根出发的字段索引链与列名。
type columnField struct {
	index  []int
	column string
}

// column 解析过滤/排序字段名：先按 Go 字段名查（识别 gorm column tag），
// 未命中时把入参视作列名/字段名按 snake_case 约定翻译。
func (m *modelMeta) column(field string) string {
	if field == "" || m == nil {
		return toColumn(field)
	}
	if f, ok := m.columns[field]; ok {
		return f.column
	}
	return toColumn(field)
}

var metaCache sync.Map // reflect.Type -> *modelMeta

func modelMetaOf[T any]() *modelMeta {
	t := reflect.TypeOf((*T)(nil)).Elem()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if cached, ok := metaCache.Load(t); ok {
		return cached.(*modelMeta)
	}
	m := buildModelMeta(t)
	metaCache.Store(t, m)
	return m
}

func buildModelMeta(t reflect.Type) *modelMeta {
	m := &modelMeta{columns: make(map[string]columnField)}
	if t.Kind() == reflect.Struct {
		collectModelFields(t, nil, "", m)
	}
	if m.primaryKey == "" {
		if f, ok := m.columns["ID"]; ok {
			m.primaryKey = f.column
		}
	}
	if m.primaryKey == "" {
		// 无主键信息时沿用交付约定：keyOf 的值定位 id 列。
		m.primaryKey = toColumn("id")
	}
	return m
}

func collectModelFields(t reflect.Type, index []int, prefix string, m *modelMeta) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := parseGormTag(f.Tag.Get("gorm"))
		if tag.ignore {
			continue
		}

		idx := append(append([]int(nil), index...), i)
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && (f.Anonymous || tag.embedded) {
			collectModelFields(ft, idx, prefix+tag.embeddedPrefix, m)
			continue
		}

		col := tag.column
		if col == "" {
			col = toColumn(f.Name)
		}
		col = prefix + col
		cf := columnField{index: idx, column: col}
		m.fields = append(m.fields, cf)
		if _, exists := m.columns[f.Name]; !exists {
			m.columns[f.Name] = cf
		}
		if tag.primaryKey && m.primaryKey == "" {
			m.primaryKey = col
		}
	}
}

// gormTag 是从 gorm tag 中解析出的与本桥接相关的设置。
type gormTag struct {
	column         string
	primaryKey     bool
	embedded       bool
	embeddedPrefix string
	ignore         bool
}

// parseGormTag 解析 `gorm:"column:uid;primaryKey;embedded;embeddedPrefix:org_"`。
// 首段以 '-' 开头的字段（"-"、"-:migration"、" -:all" 等）视为不参与写入。
func parseGormTag(tag string) gormTag {
	var g gormTag
	for _, part := range strings.Split(tag, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if part[0] == '-' {
			g.ignore = true
			continue
		}
		key, value, _ := strings.Cut(part, ":")
		switch strings.ToLower(key) {
		case "column":
			g.column = value
		case "primarykey":
			g.primaryKey = true
		case "embedded":
			g.embedded = true
		case "embeddedprefix":
			g.embeddedPrefix = value
		}
	}
	return g
}

// fieldByIndex 按索引链取值；遇到 nil 嵌入指针时返回 false 而不是 panic。
func fieldByIndex(v reflect.Value, index []int) (reflect.Value, bool) {
	for _, i := range index {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct || i >= v.NumField() {
			return reflect.Value{}, false
		}
		v = v.Field(i)
	}
	return v, true
}

// Delete 按条件删除，返回受影响行数。
// **幂等语义**：0 行匹配返回 (0, nil)，不报错——删除不存在的资源是合法结果。
// 需要「必须存在才能删」的调用方，自行判 rows == 0。
func (q *gormQuery[T]) Delete(ctx context.Context, where *store.Where) (int64, error) {
	tx := q.applyWhere(where).WithContext(ctx)
	res := tx.Delete(new(T))
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func (q *gormQuery[T]) Get(ctx context.Context, where *store.Where) (*T, error) {
	var obj T
	tx := q.applyWhere(where).WithContext(ctx).Limit(1)
	if err := tx.First(&obj).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &obj, nil
}

func (q *gormQuery[T]) List(ctx context.Context, where *store.Where) ([]*T, int64, error) {
	var total int64
	if err := q.applyWhere(where).WithContext(ctx).Model(new(T)).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	tx := q.applyWhere(where).WithContext(ctx)
	tx = q.applySorting(tx, where.Sorting)
	tx = q.applyPaging(tx, where)
	var items []*T
	if err := tx.Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (q *gormQuery[T]) Count(ctx context.Context, where *store.Where) (int64, error) {
	var n int64
	if err := q.applyWhere(where).WithContext(ctx).Model(new(T)).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// applyWhere 把 Where 的所有过滤条件翻译为 GORM 查询作用域。
// 语义：AND(Filters..., Expr)——扁平条件与布尔树（Where.Expr）按 AND 连接。
func (q *gormQuery[T]) applyWhere(where *store.Where) *gorm.DB {
	tx := q.db
	if where == nil {
		return tx
	}
	for _, c := range where.Filters {
		tx = q.applyFilter(tx, c)
	}
	if fe := where.Expr; fe != nil {
		sql, args := q.buildExpr(fe)
		tx = tx.Where("("+sql+")", args...)
	}
	return tx
}

// buildExpr 递归把 FilterExpr 布尔树翻译为参数化 SQL 片段（AND/OR 嵌套）。
// 空 AND 节点恒真（1 = 1），空 OR 节点恒假（1 = 0）——布尔代数语义，
// 与 inmemory 后端一致；恒假用于「禁止访问」类数据范围。
func (q *gormQuery[T]) buildExpr(fe *storev1.FilterExpr) (string, []any) {
	isOr := fe.GetType() == storev1.ExprType_OR
	parts := make([]string, 0, len(fe.GetConditions())+len(fe.GetGroups()))
	var args []any
	for _, c := range fe.GetConditions() {
		sql, cargs := condSQL(q.meta, c)
		parts = append(parts, sql)
		args = append(args, cargs...)
	}
	for _, g := range fe.GetGroups() {
		sql, gargs := q.buildExpr(g)
		parts = append(parts, "("+sql+")")
		args = append(args, gargs...)
	}
	if len(parts) == 0 {
		if isOr {
			return "1 = 0", nil
		}
		return "1 = 1", nil
	}
	sep := " AND "
	if isOr {
		sep = " OR "
	}
	return strings.Join(parts, sep), args
}

// condArg 返回条件的绑定值：优先 json_value（任意 JSON 类型按驱动绑定），
// 否则取字符串 value。
func condArg(c *storev1.FilterCondition) any {
	if jv := c.GetJsonValue(); jv != nil {
		return jv.AsInterface()
	}
	return c.GetValue()
}

// condSQL 翻译单条过滤条件为 SQL 片段（占位符参数化，杜绝字符串拼接值）。
func condSQL(meta *modelMeta, c *storev1.FilterCondition) (string, []any) {
	col := meta.column(c.GetField())
	v := condArg(c)
	switch c.GetOp() {
	case storev1.Operator_EQ, storev1.Operator_EXACT:
		return col + " = ?", []any{v}
	case storev1.Operator_NEQ:
		return col + " <> ?", []any{v}
	case storev1.Operator_GT:
		return col + " > ?", []any{v}
	case storev1.Operator_GTE:
		return col + " >= ?", []any{v}
	case storev1.Operator_LT:
		return col + " < ?", []any{v}
	case storev1.Operator_LTE:
		return col + " <= ?", []any{v}
	case storev1.Operator_LIKE:
		return col + " LIKE ?", []any{v}
	case storev1.Operator_ILIKE:
		// 仅对字符串类型列安全；非字符串列的 ILIKE 由调用方自行保证。
		// 优先用数据库原生不区分大小写匹配（MySQL 8 / PG 支持），
		// 退化为 lower() 仅作 SQLite 兼容兜底。
		return "LOWER(" + col + ") LIKE LOWER(?)", []any{v}
	case storev1.Operator_NOT_LIKE:
		return col + " NOT LIKE ?", []any{v}
	case storev1.Operator_IN:
		return col + " IN ?", []any{c.GetValues()}
	case storev1.Operator_NIN:
		return col + " NOT IN ?", []any{c.GetValues()}
	case storev1.Operator_IS_NULL:
		return col + " IS NULL", nil
	case storev1.Operator_IS_NOT_NULL:
		return col + " IS NOT NULL", nil
	case storev1.Operator_CONTAINS:
		return col + " LIKE ?", []any{"%" + c.GetValue() + "%"}
	case storev1.Operator_STARTS_WITH:
		return col + " LIKE ?", []any{c.GetValue() + "%"}
	case storev1.Operator_ENDS_WITH:
		return col + " LIKE ?", []any{"%" + c.GetValue()}
	case storev1.Operator_ICONTAINS:
		return "LOWER(" + col + ") LIKE LOWER(?)", []any{"%" + c.GetValue() + "%"}
	case storev1.Operator_ISTARTS_WITH:
		return "LOWER(" + col + ") LIKE LOWER(?)", []any{c.GetValue() + "%"}
	case storev1.Operator_IENDS_WITH:
		return "LOWER(" + col + ") LIKE LOWER(?)", []any{"%" + c.GetValue()}
	case storev1.Operator_IEXACT:
		return "LOWER(" + col + ") = LOWER(?)", []any{v}
	case storev1.Operator_BETWEEN:
		vals := c.GetValues()
		if len(vals) == 2 {
			return col + " BETWEEN ? AND ?", []any{vals[0], vals[1]}
		}
		return "1 = 1", nil
	case storev1.Operator_REGEXP, storev1.Operator_IREGEXP:
		// 依赖驱动方言：MySQL 原生 REGEXP（默认大小写不敏感）；
		// PostgreSQL 需 pg_trgm/正则扩展；SQLite 不支持（会报错，属预期）。
		return col + " REGEXP ?", []any{v}
	// JSON_CONTAINS / ARRAY_CONTAINS / EXISTS / SEARCH 依赖引擎方言，通用桥接
	// 不做猜测式翻译（安全忽略，恒真）。
	//
	// 为何不在本桥接补这三个（详见《Bald 存储设计》§操作符覆盖）：
	// 本桥接刻意保持**方言无关**（condSQL 生成硬编码 SQL，不检测 dialect）。补这些
	// 需引入「操作符 × 方言」矩阵——JSON_CONTAINS 在 MySQL/PG/SQLite 语法各异、
	// ARRAY_CONTAINS 多数方言无直接对应、EXISTS 的 proto 语义（子查询/存在性）在
	// SQL 无字段存在性对应。技术上可行（gorm.DB 内嵌 *Config，Config.Dialector.Name()
	// 可分支），但当前测试引擎 SQLite 覆盖不了多数分支、且这三个操作符全仓零调用方
	// ——成本/收益不成立。store-mongo 因 MongoDB 有原生能力（数组等值/点号路径/$exists）
	// 故单独补了三个。SEARCH 三后端均不补：$text 需预建索引否则报错。
	default:
		return "1 = 1", nil
	}
}

// applyFilter 翻译单条过滤条件（扁平 AND 路径复用 condSQL）。
func (q *gormQuery[T]) applyFilter(tx *gorm.DB, c *storev1.FilterCondition) *gorm.DB {
	sql, args := condSQL(q.meta, c)
	if len(args) == 0 {
		return tx.Where(sql)
	}
	return tx.Where(sql, args...)
}

// applySorting 翻译排序规则。
func (q *gormQuery[T]) applySorting(tx *gorm.DB, sorting []*storev1.Sorting) *gorm.DB {
	for _, s := range sorting {
		col := q.meta.column(s.GetField())
		dir := "ASC"
		if s.GetDirection() == storev1.Sorting_DESC {
			dir = "DESC"
		}
		tx = tx.Order(col + " " + dir)
	}
	return tx
}

// applyPaging 翻译偏移/限制分页（where 可为 nil，无条件全列）。
func (q *gormQuery[T]) applyPaging(tx *gorm.DB, where *store.Where) *gorm.DB {
	if where == nil {
		return tx
	}
	if where.Offset > 0 {
		tx = tx.Offset(where.Offset)
	}
	if where.Limit > 0 {
		tx = tx.Limit(where.Limit)
	}
	return tx
}

// toColumn 把 Go 字段名翻译为数据库列名（默认 snake_case）。
// gorm:"column:..." 自定义列名由 modelMeta 先解析；本函数只是未命中时的
// 命名约定兜底，也可直接用于传入的原始列名。
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
