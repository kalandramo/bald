package baldgorm

// gorm_gaps_test.go —— 批6 行级覆盖：把 conn/gorm/provider 里零命中的分支
// （option、错误注入、元数据边界、操作符翻译、段驱动契约）逐个钉住。

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/glebarez/sqlite"
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestNewGormProvider_Panics 两个必填依赖缺失都必须 panic（装配期 fail-fast）。
func TestNewGormProvider_Panics(t *testing.T) {
	assert.Panics(t, func() { NewGormProvider[User](nil, func(*User) string { return "" }) })
	db := NewTestDB(t)
	assert.Panics(t, func() { NewGormProvider[User](db, nil) })
}

// TestProviderCloseAndQueryMigrate 关闭与查询级 AutoMigrate（含零模型默认 T）。
func TestProviderCloseAndQueryMigrate(t *testing.T) {
	p := newTestProvider(t)
	require.NoError(t, p.Close())

	q, err := p.DB(context.Background())
	require.NoError(t, err)
	require.NoError(t, q.Migrate(context.Background()))
	require.NoError(t, q.Migrate(context.Background(), &User{}))
}

// TestOpen_WithGormConfig 整包覆盖 gorm.Config（高级逃生门）。
func TestOpen_WithGormConfig(t *testing.T) {
	db := openTestDB(t, WithGormConfig(&gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
	}))
	if !db.Config.SkipDefaultTransaction {
		t.Fatal("WithGormConfig 未生效")
	}
	require.NoError(t, db.AutoMigrate(&User{}))
	require.NoError(t, db.Create(&User{ID: "cfg", Name: "n"}).Error)
}

// TestOpen_GormOpenError 连接初始化失败必须被包装为明确错误。
func TestOpen_GormOpenError(t *testing.T) {
	badPath := filepath.Join(t.TempDir(), "missing-dir", "x.db")
	_, err := Open(WithDSN(badPath), WithDriver("sqlite"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "baldgorm: open sqlite database")
}

// gapConnPool 模拟「没有 *sql.DB 连接池」的 dialector 形态（如某些
// prepared-stmt-only 驱动）：gorm.DB.DB() 对它返回 ErrInvalidDB。
type gapConnPool struct{}

func (gapConnPool) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	return nil, errors.New("gap: prepared statements unsupported")
}

func (gapConnPool) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errors.New("gap: exec unsupported")
}

func (gapConnPool) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("gap: query unsupported")
}

func (gapConnPool) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return &sql.Row{}
}

// gapDelayedPoolDialector 先完成 sqlite 初始化（建库、注册回调），再把
// 连接池替换成非 *sql.DB 的形态。
type gapDelayedPoolDialector struct{ gorm.Dialector }

func (d gapDelayedPoolDialector) Initialize(db *gorm.DB) error {
	if err := d.Dialector.Initialize(db); err != nil {
		return err
	}
	db.ConnPool = gapConnPool{}
	return nil
}

func registerGapDialector() {
	RegisterDialector("gap-delayed-pool", func(string) gorm.Dialector {
		return gapDelayedPoolDialector{Dialector: sqlite.Open("file::memory:?cache=shared")}
	})
}

// TestApplyPool_NoSQLPool 无连接池形态忽略池参数而不是 panic。
func TestApplyPool_NoSQLPool(t *testing.T) {
	registerGapDialector()
	db, err := Open(
		WithDriver("gap-delayed-pool"),
		WithDSN("file::memory:?cache=shared"),
		WithConfig(&bootstrapv1.Database_SQL{
			MaxIdleConnections:           1,
			MaxOpenConnections:           2,
			ConnectionMaxLifetimeSeconds: 10,
		}),
	)
	require.NoError(t, err)
	_, err = db.DB()
	require.Error(t, err, "前提：该 dialector 的 ConnPool 不是 *sql.DB")
}

// TestAppProviderCleanup_NoSQLPool cleanup 在无 *sql.DB 时静默返回 nil。
func TestAppProviderCleanup_NoSQLPool(t *testing.T) {
	registerGapDialector()
	sec := &bootstrapv1.Database_SQL{Driver: "sqlite", Source: "file::memory:?cache=shared"}
	product, cleanup, err := AppProvider(WithDriver("gap-delayed-pool")).Build(
		context.Background(),
		appkit.ApplyScope{Config: &bootstrapv1.BootstrapConfig{
			Database: &bootstrapv1.Database{Sql: sec},
		}},
		appkit.Node{},
	)
	require.NoError(t, err)
	require.NotNil(t, product)
	require.NotNil(t, cleanup)
	require.NoError(t, cleanup(context.Background()))
}

// --- modelMeta 边界 ---

type gapPrefixFields struct {
	Code string `gorm:"column:code"`
}

type gapEdgeModel struct {
	ID      string `gorm:"primaryKey"`
	hidden  string
	PtrName *string
	PF      gapPrefixFields `gorm:"embedded;embeddedPrefix:org_"`
}

// TestModelMeta_Edges 覆盖：未导出字段跳过、指针字段解引用、嵌入前缀、空 meta。
func TestModelMeta_Edges(t *testing.T) {
	inst := gapEdgeModel{hidden: "memory-only"}
	if inst.hidden != "memory-only" {
		t.Fatal("前提不成立：未导出字段应只在内存中可见")
	}

	m := modelMetaOf[gapEdgeModel]()
	if got := m.columns["PtrName"].column; got != "ptr_name" {
		t.Errorf("PtrName 列名 = %q, want ptr_name", got)
	}
	if got := m.columns["Code"].column; got != "org_code" {
		t.Errorf("嵌入前缀未生效：Code 列名 = %q, want org_code", got)
	}
	if _, ok := m.columns["hidden"]; ok {
		t.Error("未导出字段不应进入映射")
	}

	var nilMeta *modelMeta
	if got := nilMeta.column("UserName"); got != "user_name" {
		t.Errorf("nil meta column = %q, want user_name", got)
	}
	if got := m.column(""); got != "" {
		t.Errorf("空字段名 column = %q, want \"\"", got)
	}
	if got := toColumn(""); got != "" {
		t.Errorf("toColumn(\"\") = %q", got)
	}

	// 指针 T 归一后与值类型共享同一份 meta 缓存。
	if modelMetaOf[*User]() != modelMetaOf[User]() {
		t.Error("指针类型未归一到同一 meta")
	}
}

// TestFieldByIndex_Edges 指针解引用 / nil 指针 / 非结构体 / 越界四类边界。
func TestFieldByIndex_Edges(t *testing.T) {
	type leaf struct{ V string }
	type mid struct{ L *leaf }
	type root struct{ M *mid }

	v := reflect.ValueOf(&root{M: &mid{L: &leaf{V: "x"}}})
	got, ok := fieldByIndex(v, []int{0, 0, 0})
	if !ok || got.String() != "x" {
		t.Errorf("多级指针解码 = %v ok=%v, want x", got, ok)
	}

	if _, ok := fieldByIndex(reflect.ValueOf(&root{}), []int{0, 0}); ok {
		t.Error("nil 嵌入指针应返回 false")
	}
	if _, ok := fieldByIndex(reflect.ValueOf("str"), []int{0}); ok {
		t.Error("非结构体应返回 false")
	}
	if _, ok := fieldByIndex(reflect.ValueOf(leaf{}), []int{5}); ok {
		t.Error("越界索引应返回 false")
	}
}

// TestToUpdateMap_NilEmbedded 嵌入指针为 nil 时跳过该列而不是 panic。
func TestToUpdateMap_NilEmbedded(t *testing.T) {
	type noteFields struct{ Note string }
	type optEmbedDoc struct {
		ID    string      `gorm:"primaryKey"`
		Extra *noteFields `gorm:"embedded"`
		Name  string
	}
	m := toUpdateMap(&optEmbedDoc{ID: "1", Name: "n"}, modelMetaOf[optEmbedDoc]())
	assert.Equal(t, map[string]any{"name": "n"}, m)
}

// uniqueUser 用唯一索引制造 Update 冲突。
type uniqueUser struct {
	ID   string `gorm:"primaryKey"`
	Name string `gorm:"uniqueIndex"`
}

// TestUpdate_UniqueViolation Update 撞唯一约束统一映射为 store.ErrConflict。
func TestUpdate_UniqueViolation(t *testing.T) {
	p := newTypedProvider[uniqueUser](t, func(u *uniqueUser) string { return u.ID })
	repo := store.NewStore[uniqueUser](p)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, &uniqueUser{ID: "1", Name: "a"}))
	require.NoError(t, repo.Create(ctx, &uniqueUser{ID: "2", Name: "b"}))

	_, err := repo.Update(ctx, &uniqueUser{ID: "2", Name: "a"})
	assert.ErrorIs(t, err, store.ErrConflict)
}

// TestUpdate_CompositeKeyUnresolvable 复合主键经 nil 嵌入指针解析失败时报明确错误。
func TestUpdate_CompositeKeyUnresolvable(t *testing.T) {
	type pkPart struct {
		OrgID string `gorm:"column:org_id;primaryKey"`
	}
	type compositePtrDoc struct {
		ID  string  `gorm:"primaryKey"`
		Emb *pkPart `gorm:"embedded"`
	}
	q := &gormQuery[compositePtrDoc]{
		db:    NewTestDB(t),
		keyOf: func(*compositePtrDoc) string { return "" },
		meta:  modelMetaOf[compositePtrDoc](),
	}
	_, err := q.Update(context.Background(), &compositePtrDoc{ID: "1"}, nil)
	require.ErrorContains(t, err, `cannot resolve composite primary key column "org_id"`)
}

// TestList_FindError Count 成功而 Find 失败时错误必须上抛（不被 total 掩盖）。
func TestList_FindError(t *testing.T) {
	db := NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&User{}))
	require.NoError(t, db.Create(&User{ID: "1", Name: "a"}).Error)

	findErr := errors.New("find boom")
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("gaps_fail_find", func(tx *gorm.DB) {
		if _, isCount := tx.Statement.Dest.(*int64); !isCount {
			tx.AddError(findErr)
		}
	}))

	p := NewGormProvider[User](db, func(u *User) string { return u.ID })
	_, _, err := store.NewStore[User](p).List(context.Background(), nil)
	assert.ErrorIs(t, err, findErr)
}

// --- Where 翻译 ---

// TestCondSQL_AllOperators 覆盖每个操作符的 SQL 形状与绑定值数量。
func TestCondSQL_AllOperators(t *testing.T) {
	meta := modelMetaOf[User]()
	val := func(op storev1.Operator, v string) *storev1.FilterCondition {
		return &storev1.FilterCondition{Field: "Age", Op: op, ValueOneof: &storev1.FilterCondition_Value{Value: v}}
	}
	noVal := func(op storev1.Operator) *storev1.FilterCondition {
		return &storev1.FilterCondition{Field: "Age", Op: op}
	}
	vals := func(op storev1.Operator, vs ...string) *storev1.FilterCondition {
		return &storev1.FilterCondition{Field: "Age", Op: op, Values: vs}
	}
	cases := []struct {
		name     string
		cond     *storev1.FilterCondition
		wantSQL  string
		wantArgs int
	}{
		{"EQ", val(storev1.Operator_EQ, "1"), "age = ?", 1},
		{"EXACT", val(storev1.Operator_EXACT, "1"), "age = ?", 1},
		{"NEQ", val(storev1.Operator_NEQ, "1"), "age <> ?", 1},
		{"GT", val(storev1.Operator_GT, "1"), "age > ?", 1},
		{"GTE", val(storev1.Operator_GTE, "1"), "age >= ?", 1},
		{"LT", val(storev1.Operator_LT, "1"), "age < ?", 1},
		{"LTE", val(storev1.Operator_LTE, "1"), "age <= ?", 1},
		{"LIKE", val(storev1.Operator_LIKE, "1"), "age LIKE ?", 1},
		{"ILIKE", val(storev1.Operator_ILIKE, "1"), "LOWER(age) LIKE LOWER(?)", 1},
		{"NOT_LIKE", val(storev1.Operator_NOT_LIKE, "1"), "age NOT LIKE ?", 1},
		{"IN", vals(storev1.Operator_IN, "1", "2"), "age IN ?", 1},
		{"NIN", vals(storev1.Operator_NIN, "1"), "age NOT IN ?", 1},
		{"IS_NULL", noVal(storev1.Operator_IS_NULL), "age IS NULL", 0},
		{"IS_NOT_NULL", noVal(storev1.Operator_IS_NOT_NULL), "age IS NOT NULL", 0},
		{"CONTAINS", val(storev1.Operator_CONTAINS, "1"), "age LIKE ?", 1},
		{"STARTS_WITH", val(storev1.Operator_STARTS_WITH, "1"), "age LIKE ?", 1},
		{"ENDS_WITH", val(storev1.Operator_ENDS_WITH, "1"), "age LIKE ?", 1},
		{"ICONTAINS", val(storev1.Operator_ICONTAINS, "1"), "LOWER(age) LIKE LOWER(?)", 1},
		{"ISTARTS_WITH", val(storev1.Operator_ISTARTS_WITH, "1"), "LOWER(age) LIKE LOWER(?)", 1},
		{"IENDS_WITH", val(storev1.Operator_IENDS_WITH, "1"), "LOWER(age) LIKE LOWER(?)", 1},
		{"IEXACT", val(storev1.Operator_IEXACT, "1"), "LOWER(age) = LOWER(?)", 1},
		{"BETWEEN", vals(storev1.Operator_BETWEEN, "1", "2"), "age BETWEEN ? AND ?", 2},
		{"BETWEEN 值不足恒真", vals(storev1.Operator_BETWEEN, "1"), "1 = 1", 0},
		{"REGEXP", val(storev1.Operator_REGEXP, "1"), "age REGEXP ?", 1},
		{"IREGEXP", val(storev1.Operator_IREGEXP, "1"), "age REGEXP ?", 1},
		{"方言相关操作符恒真", val(storev1.Operator_JSON_CONTAINS, "1"), "1 = 1", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sqlText, args := condSQL(meta, c.cond)
			assert.Equal(t, c.wantSQL, sqlText)
			assert.Len(t, args, c.wantArgs)
		})
	}
}

// TestCondArg_JSONValue json_value 优先于字符串 value（任意 JSON 类型直传驱动）。
func TestCondArg_JSONValue(t *testing.T) {
	c := &storev1.FilterCondition{
		Field: "Age",
		Op:    storev1.Operator_EQ,
		ValueOneof: &storev1.FilterCondition_JsonValue{
			JsonValue: structpb.NewNumberValue(3),
		},
	}
	sqlText, args := condSQL(modelMetaOf[User](), c)
	assert.Equal(t, "age = ?", sqlText)
	require.Len(t, args, 1)
	assert.Equal(t, 3.0, args[0])
}

// TestWhereHelpers_NilAndEmpty 覆盖 where==nil、空布尔树、无参过滤与分页边角。
func TestWhereHelpers_NilAndEmpty(t *testing.T) {
	q := &gormQuery[User]{db: NewTestDB(t), meta: modelMetaOf[User]()}

	if got := q.applyWhere(nil); got != q.db {
		t.Error("applyWhere(nil) 应原样返回 db")
	}

	if sqlText, args := q.buildExpr(&storev1.FilterExpr{}); sqlText != "1 = 1" || args != nil {
		t.Errorf("空 AND = %q %v, want 1 = 1", sqlText, args)
	}
	if sqlText, _ := q.buildExpr(&storev1.FilterExpr{Type: storev1.ExprType_OR}); sqlText != "1 = 0" {
		t.Errorf("空 OR = %q, want 1 = 0", sqlText)
	}

	noArgs := &storev1.FilterCondition{Field: "Age", Op: storev1.Operator_IS_NULL}
	if tx := q.applyFilter(q.db.Session(&gorm.Session{DryRun: true}), noArgs); tx == nil {
		t.Error("applyFilter 无参条件返回 nil")
	}

	if got := q.applyPaging(q.db, nil); got != q.db {
		t.Error("applyPaging(nil) 应原样返回")
	}
	if tx := q.applyPaging(q.db.Session(&gorm.Session{DryRun: true}), &store.Where{Offset: 5}); tx == nil {
		t.Error("applyPaging(offset) 返回 nil")
	}
}

// TestIsUniqueViolation_Nil nil 错误永远不是唯一冲突。
func TestIsUniqueViolation_Nil(t *testing.T) {
	assert.False(t, isUniqueViolation(nil))
	assert.True(t, isUniqueViolation(errors.New("UNIQUE constraint failed")))
}

// --- provider 段驱动面 ---

// TestAppProviderSegmentsAndPlan 段路径与节点声明是外部装配契约。
func TestAppProviderSegmentsAndPlan(t *testing.T) {
	p := AppProvider()
	assert.Equal(t, []string{"database.sql"}, p.Segments())

	nodes, err := p.Plan(context.Background(), appkit.PlanScope{})
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	n := nodes[0]
	assert.Equal(t, "database.sql", string(n.Key))
	assert.Equal(t, []string{"database.sql"}, n.Segments)
	assert.Equal(t, appkit.PhaseBootstrap, n.Phase)
	assert.Equal(t, appkit.PriorityDatabase, n.Priority)
	assert.Equal(t, appkit.ReloadRestart, n.Reload)
	assert.Equal(t, "appkit:database-clients", n.EffectName)
}

// TestAppProviderBuild_MissingSection 段缺失（引擎匹配错位）时报 bug 级错误。
func TestAppProviderBuild_MissingSection(t *testing.T) {
	_, _, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{},
	}, appkit.Node{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database.sql section is missing")
}

// 结构性零覆盖（登记备查）：
//   - testutil.go NewTestDB 的 Open 失败分支：入参是 t.TempDir()（测试框架保证
//     存在且可写）+ 内置 sqlite 驱动，失败只可能来自测试基础设施本身损坏；
//     注入故障需全局替换 sqlite 注册表，会污染同模块其它用例。
