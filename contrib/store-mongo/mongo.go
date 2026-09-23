// Package baldmongo 是 bald 存储层的 MongoDB 桥接子模块（独立 go.mod）。
//
// 把 mongo-driver v2 的 *mongo.Database 适配为 bald 核心的 store.DBProvider[T] +
// store.Queryable[T]，使业务能以统一的泛型 Store[T] 姿态访问 MongoDB，而 bald
// 核心不引入任何 MongoDB 依赖（遵循 P5 零后端耦合）。
//
// 用法：
//
//	client, _ := mongo.Connect(options.Client().ApplyURI("mongodb://localhost:27017"))
//	provider := baldmongo.NewMongoProvider[User](client.Database("demo"), "users", func(u *User) string { return u.ID })
//	repo := store.NewStore[User](provider)
//
// 字段映射：Where.Filters 的 Field 是 DTO/Entity 的 Go 字段名，经 toField 翻译成
// BSON 字段名（默认 snake_case，与 store-gorm 的 toColumn 约定一致），使同一 Where
// 语义在内存/GORM/Mongo 三后端表现相同。
package baldmongo

import (
	"context"
	"reflect"
	"strconv"
	"strings"

	"github.com/kalandramo/bald/pkg/store"
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Provider 是 MongoDB 版 DBProvider[T]。
// 持有 *mongo.Database、集合名与实体主键提取函数；DB() 返回绑定到具体集合的 Queryable。
type Provider[T any] struct {
	db         *mongo.Database
	collection string
	keyOf      func(*T) string
	keyField   string // 主键 BSON 字段名（默认 "id"）
}

// Option 配置 Provider。
type Option[T any] func(*Provider[T])

// WithKeyField 设置主键 BSON 字段名（默认 "id"，即 Go 字段 ID 的 snake_case）。
// 实体主键字段名非 ID 时用此项覆盖，避免 Get/Update/Delete 定位到不存在的字段。
func WithKeyField[T any](field string) Option[T] {
	return func(p *Provider[T]) { p.keyField = field }
}

// NewMongoProvider 构造 MongoDB 提供者。
//   - db: 已连接/已选的 *mongo.Database。
//   - collection: 集合名。
//   - keyOf: 从实体提取主键，用于 Get/Update/Delete 的唯一定位（避免全表扫描）。
func NewMongoProvider[T any](db *mongo.Database, collection string, keyOf func(*T) string, opts ...Option[T]) *Provider[T] {
	if db == nil {
		panic("baldmongo: db must not be nil")
	}
	if collection == "" {
		panic("baldmongo: collection must not be empty")
	}
	if keyOf == nil {
		panic("baldmongo: keyOf must not be nil")
	}
	p := &Provider[T]{db: db, collection: collection, keyOf: keyOf, keyField: "id"}
	for _, fn := range opts {
		fn(p)
	}
	return p
}

// DB 返回 MongoDB Queryable（会话绑定到 T 的集合）。
func (p *Provider[T]) DB(_ context.Context) (store.Queryable[T], error) {
	return &mongoQuery[T]{
		col:      p.db.Collection(p.collection),
		keyOf:    p.keyOf,
		keyField: p.keyField,
		fieldTypes: fieldTypeMap[T](),
	}, nil
}

// Close 空实现（mongo 客户端连接池由调用方管理生命周期）。
func (p *Provider[T]) Close() error { return nil }

// Migrate 确保集合存在（MongoDB 无 schema，仅创建集合 + 主键唯一索引）。
// 传入 models 参数被忽略（对齐 store-gorm 的接口形态，Mongo 无表结构概念）。
func (p *Provider[T]) Migrate(ctx context.Context, _ ...any) error {
	err := p.db.CreateCollection(ctx, p.collection)
	if err != nil && !isNamespaceExists(err) {
		return err
	}
	_, err = p.db.Collection(p.collection).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: p.keyField, Value: 1}},
		Options: options.Index().SetUnique(true).SetName(p.keyField + "_unique"),
	})
	return err
}

// mongoQuery 实现 store.Queryable[T]，把 Where 翻译成 BSON filter。
type mongoQuery[T any] struct {
	col        *mongo.Collection
	keyOf      func(*T) string
	keyField   string
	fieldTypes map[string]reflect.Type // BSON 字段名 → Go 字段类型（用于值类型转换）
}

// Migrate 幂等空操作（集合由 Provider.Migrate 创建；会话级无独立语义）。
func (q *mongoQuery[T]) Migrate(_ context.Context, _ ...any) error { return nil }

func (q *mongoQuery[T]) Create(ctx context.Context, obj *T) error {
	_, err := q.col.InsertOne(ctx, obj)
	if err != nil {
		if isDuplicateKey(err) {
			return store.ErrConflict
		}
		return err
	}
	return nil
}

// Update 更新一条记录，返回受影响行数。
// **幂等语义**（与 Delete 一致）：0 行匹配返回 (0, nil)，不报错——Mongo 的
// UpdateOne 匹配 0 文档是正常结果，驱动不擅自增加底层没有的错误。
//
// 返回的是 `MatchedCount`（**匹配行数**）——注意这与 gorm 后端返回的受影响行数
// 语义不同：Mongo 的 MatchedCount 对「更新到相同值」仍返回 1（记录存在即算匹配），
// 因此 Mongo 后端下 `rows == 0` 反而**可以**作为「记录不存在」的判据。这是跨后端
// 差异，业务若依赖该判据需注意后端选择（详见 store.Store.Update 注释）。
func (q *mongoQuery[T]) Update(ctx context.Context, obj *T) (int64, error) {
	k := q.keyOf(obj)
	// 用 $set 更新全部字段（排除主键，防止主键被改写导致文档错位）。
	set := toUpdateDoc(obj, q.keyField)
	res, err := q.col.UpdateOne(ctx, bson.D{{Key: q.keyField, Value: k}}, bson.D{{Key: "$set", Value: set}})
	if err != nil {
		if isDuplicateKey(err) {
			return 0, store.ErrConflict
		}
		return 0, err
	}
	return res.MatchedCount, nil
}

// Delete 按条件删除，返回受影响行数。
// **幂等语义**：0 行匹配返回 (0, nil)，不报错——删除不存在的资源是合法结果。
// 需要「必须存在才能删」的调用方，自行判 rows == 0。
func (q *mongoQuery[T]) Delete(ctx context.Context, where *store.Where) (int64, error) {
	res, err := q.col.DeleteMany(ctx, q.buildFilter(where))
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

func (q *mongoQuery[T]) Get(ctx context.Context, where *store.Where) (*T, error) {
	var obj T
	err := q.col.FindOne(ctx, q.buildFilter(where)).Decode(&obj)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &obj, nil
}

func (q *mongoQuery[T]) List(ctx context.Context, where *store.Where) ([]*T, int64, error) {
	filter := q.buildFilter(where)
	total, err := q.col.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	findOpts := options.Find()
	if where != nil {
		// 仅在确有排序时设置——SetSort(nil/空) 会致 mongo-driver marshal 失败。
		if s := buildSort(where.Sorting); s != nil {
			findOpts.SetSort(s)
		}
		if where.Offset > 0 {
			findOpts.SetSkip(int64(where.Offset))
		}
		if where.Limit > 0 {
			findOpts.SetLimit(int64(where.Limit))
		}
	}
	cur, err := q.col.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = cur.Close(ctx) }()
	var items []*T
	if err := cur.All(ctx, &items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (q *mongoQuery[T]) Count(ctx context.Context, where *store.Where) (int64, error) {
	return q.col.CountDocuments(ctx, q.buildFilter(where))
}

// buildFilter 把 Where 翻译为 BSON filter（AND(Filters..., Expr)）。
func (q *mongoQuery[T]) buildFilter(where *store.Where) bson.D {
	if where == nil {
		return bson.D{}
	}
	var and bson.A
	for _, c := range where.Filters {
		and = append(and, q.condFilter(c))
	}
	if fe := where.Expr; fe != nil {
		and = append(and, q.exprFilter(fe))
	}
	if len(and) == 0 {
		return bson.D{}
	}
	if len(and) == 1 {
		return and[0].(bson.D)
	}
	return bson.D{{Key: "$and", Value: and}}
}

// exprFilter 递归把 FilterExpr 布尔树翻译为 BSON filter。
// 空 AND 节点恒真（空 filter），空 OR 节点恒假（$expr:false）——与 inmemory/gorm 后端一致。
func (q *mongoQuery[T]) exprFilter(fe *storev1.FilterExpr) bson.D {
	isOr := fe.GetType() == storev1.ExprType_OR
	var parts bson.A
	for _, c := range fe.GetConditions() {
		parts = append(parts, q.condFilter(c))
	}
	for _, g := range fe.GetGroups() {
		parts = append(parts, q.exprFilter(g))
	}
	if len(parts) == 0 {
		if isOr {
			// 空 OR 恒假：$expr 恒假条件，任何文档都不匹配。
			return bson.D{{Key: "$expr", Value: false}}
		}
		return bson.D{} // 空 AND 恒真
	}
	if len(parts) == 1 {
		return parts[0].(bson.D)
	}
	op := "$and"
	if isOr {
		op = "$or"
	}
	return bson.D{{Key: op, Value: parts}}
}

// condFilter 翻译单条过滤条件为 BSON filter（按字段类型转换比较值）。
func (q *mongoQuery[T]) condFilter(c *storev1.FilterCondition) bson.D {
	field := toField(c.GetField())
	v := q.condArg(c)
	sv := condStr(c) // 字符串形态，供 regex 场景使用
	switch c.GetOp() {
	case storev1.Operator_EQ, storev1.Operator_EXACT:
		return bson.D{{Key: field, Value: v}}
	case storev1.Operator_NEQ:
		return bson.D{{Key: field, Value: bson.D{{Key: "$ne", Value: v}}}}
	case storev1.Operator_GT:
		return bson.D{{Key: field, Value: bson.D{{Key: "$gt", Value: v}}}}
	case storev1.Operator_GTE:
		return bson.D{{Key: field, Value: bson.D{{Key: "$gte", Value: v}}}}
	case storev1.Operator_LT:
		return bson.D{{Key: field, Value: bson.D{{Key: "$lt", Value: v}}}}
	case storev1.Operator_LTE:
		return bson.D{{Key: field, Value: bson.D{{Key: "$lte", Value: v}}}}
	case storev1.Operator_IN:
		return bson.D{{Key: field, Value: bson.D{{Key: "$in", Value: q.coerceSlice(field, c.GetValues())}}}}
	case storev1.Operator_NIN:
		return bson.D{{Key: field, Value: bson.D{{Key: "$nin", Value: q.coerceSlice(field, c.GetValues())}}}}
	case storev1.Operator_LIKE, storev1.Operator_CONTAINS:
		return regexFilter(field, regexpQuote(sv), "")
	case storev1.Operator_ILIKE, storev1.Operator_ICONTAINS:
		return regexFilter(field, regexpQuote(sv), "i") // i = case-insensitive
	case storev1.Operator_IEXACT:
		return bson.D{{Key: field, Value: bson.D{
			{Key: "$regex", Value: "^" + regexpQuote(sv) + "$"}, {Key: "$options", Value: "i"}}}}
	case storev1.Operator_NOT_LIKE:
		return bson.D{{Key: field, Value: bson.D{{Key: "$not", Value: bson.D{{Key: "$regex", Value: regexpQuote(sv)}}}}}}
	case storev1.Operator_STARTS_WITH:
		return regexFilter(field, "^"+regexpQuote(sv), "")
	case storev1.Operator_ISTARTS_WITH:
		return regexFilter(field, "^"+regexpQuote(sv), "i")
	case storev1.Operator_ENDS_WITH:
		return regexFilter(field, regexpQuote(sv)+"$", "")
	case storev1.Operator_IENDS_WITH:
		return regexFilter(field, regexpQuote(sv)+"$", "i")
	case storev1.Operator_IS_NULL:
		return bson.D{{Key: field, Value: bson.D{{Key: "$in", Value: []any{nil, ""}}}}}
	case storev1.Operator_IS_NOT_NULL:
		return bson.D{{Key: field, Value: bson.D{{Key: "$nin", Value: []any{nil, ""}}}}}
	case storev1.Operator_REGEXP:
		return regexFilter(field, sv, "")
	case storev1.Operator_IREGEXP:
		return regexFilter(field, sv, "i")
	case storev1.Operator_BETWEEN:
		vals := c.GetValues()
		if len(vals) != 2 {
			return bson.D{{Key: "$expr", Value: false}} // 参数不足 → 恒假
		}
		return bson.D{{Key: field, Value: bson.D{{Key: "$gte", Value: vals[0]}, {Key: "$lte", Value: vals[1]}}}}
	default:
		// 未支持的操作符（JSON_CONTAINS/ARRAY_CONTAINS/EXISTS/SEARCH）：返回恒真条件，
		// 保守放行而非静默查空（与 inmemory 的"宁缺勿假"取向不同，此处避免"配了条件
		// 却查不到"的更坏后果）。已在设计文档标注该差异。
		return bson.D{}
	}
}

// regexFilter 构造 $regex 过滤器（options 为空则省略）。
func regexFilter(field, pattern, opts string) bson.D {
	inner := bson.D{{Key: "$regex", Value: pattern}}
	if opts != "" {
		inner = append(inner, bson.E{Key: "$options", Value: opts})
	}
	return bson.D{{Key: field, Value: inner}}
}

// condArg 返回条件绑定值：优先 json_value（typed，直接用），否则按**实体字段类型**
// 转换字符串 value。
//
// ⚠️ **为什么按字段类型转换**：`FilterCondition.value` 是字符串（如 `Gte("age", "25")`），
// 而 MongoDB 强类型、无 SQL 隐式转换——字符串 "25" 匹配不到 int 字段 25。gorm 后端靠
// 数据库类型亲和性隐式转换、inmemory 后端用 cmpNum 解析，Mongo 二者皆无，故桥接层按
// 实体 schema（`fieldTypeMap`）把字符串转为字段的 Go 类型（如 age→int 时 "25"→25），
// 保证同一 Where 语义跨三后端一致。字段类型未知（如未在实体上声明）时原样透传字符串。
func (q *mongoQuery[T]) condArg(c *storev1.FilterCondition) any {
	if jv := c.GetJsonValue(); jv != nil {
		return jv.AsInterface()
	}
	return coerceByType(c.GetValue(), q.fieldTypes[toField(c.GetField())])
}

// coerceSlice 对字符串切片按字段类型逐项转换（供 IN/NIN 用）。
func (q *mongoQuery[T]) coerceSlice(field string, vals []string) bson.A {
	ft := q.fieldTypes[field]
	out := make(bson.A, 0, len(vals))
	for _, v := range vals {
		out = append(out, coerceByType(v, ft))
	}
	return out
}

// coerceByType 把字符串值转为目标 Go 类型（int/int64/float64/bool），
// 转换失败或类型非标量时原样返回字符串。
func coerceByType(s string, t reflect.Type) any {
	if t == nil || s == "" {
		return s
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			return n
		}
	case reflect.Float32, reflect.Float64:
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	case reflect.Bool:
		if b, err := strconv.ParseBool(s); err == nil {
			return b
		}
	}
	return s
}

// fieldTypeMap 反射 T 的导出字段，构造 BSON 字段名 → Go 字段类型映射，
// 供 condArg 做 schema-aware 值转换。
func fieldTypeMap[T any]() map[string]reflect.Type {
	var zero T
	t := reflect.TypeOf(zero)
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	m := make(map[string]reflect.Type)
	if t == nil || t.Kind() != reflect.Struct {
		return m
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if name := strings.TrimSpace(strings.SplitN(f.Tag.Get("bson"), ",", 2)[0]); name == "-" {
			continue
		}
		m[toField(f.Name)] = f.Type
	}
	return m
}

// condStr 返回条件的字符串形态（regex 场景用；json_value 转为 JSON 文本）。
func condStr(c *storev1.FilterCondition) string {
	if jv := c.GetJsonValue(); jv != nil {
		return jv.String()
	}
	return c.GetValue()
}

// toUpdateDoc 反射实体为更新文档（BSON 字段名 → 值），剔除主键字段。
// 对齐 store-gorm 的 toMapExcludeKey：写入全部导出字段（含零值），不改主键。
func toUpdateDoc(obj any, keyField string) bson.D {
	v := reflect.ValueOf(obj).Elem()
	t := v.Type()
	doc := bson.D{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if name := strings.TrimSpace(strings.SplitN(f.Tag.Get("bson"), ",", 2)[0]); name == "-" {
			continue
		}
		field := toField(f.Name)
		if field == keyField {
			continue
		}
		doc = append(doc, bson.E{Key: field, Value: v.Field(i).Interface()})
	}
	return doc
}

// buildSort 把 Sorting 翻译为 BSON 排序文档。
func buildSort(sorting []*storev1.Sorting) bson.D {
	if len(sorting) == 0 {
		return nil
	}
	doc := bson.D{}
	for _, s := range sorting {
		dir := 1
		if s.GetDirection() == storev1.Sorting_DESC {
			dir = -1
		}
		doc = append(doc, bson.E{Key: toField(s.GetField()), Value: dir})
	}
	return doc
}

// toField 把 Go 字段名翻译为 BSON 字段名（默认 snake_case），与 store-gorm 的
// toColumn 约定一致，使同一 Where 语义跨后端表现相同。
func toField(field string) string {
	if field == "" {
		return field
	}
	runes := []rune(field)
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

// regexpQuote 转义正则元字符，避免用户输入被当作正则语法。
func regexpQuote(s string) string {
	return regexpQuoteReplacer.Replace(s)
}

var regexpQuoteReplacer = strings.NewReplacer(
	`\`, `\\`, `.`, `\.`, `+`, `\+`, `*`, `\*`, `?`, `\?`,
	`(`, `\(`, `)`, `\)`, `[`, `\[`, `]`, `\]`, `{`, `\{`, `}`, `\}`,
	`^`, `\^`, `$`, `\$`, `|`, `\|`,
)

// isDuplicateKey 判断是否为唯一键冲突（E11000）。
func isDuplicateKey(err error) bool {
	return err != nil && strings.Contains(err.Error(), "E11000")
}

// isNamespaceExists 判断集合已存在（NamespaceExists，Code 48）。
func isNamespaceExists(err error) bool {
	if err == nil {
		return false
	}
	if ce, ok := err.(mongo.CommandError); ok {
		return ce.Code == 48
	}
	return strings.Contains(err.Error(), "NamespaceExists")
}
