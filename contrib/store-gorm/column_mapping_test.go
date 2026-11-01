package baldgorm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// taggedUser 覆盖两类非默认映射：自定义列名与非 ID 主键。
type taggedUser struct {
	UserID  string `gorm:"column:uid;primaryKey"`
	Login   string `gorm:"column:login_name"`
	OrgID   string `gorm:"column:org_id"`
	Ignore  string `gorm:"-"`
	Regular int
}

// AuditFields 是非匿名字段场景下的嵌入结构体。
type AuditFields struct {
	CreatedBy string `gorm:"column:created_by"`
}

// embeddedDoc 验证嵌入结构体的字段按列展开，而不是被当作一个列值。
type embeddedDoc struct {
	ID          string `gorm:"primaryKey"`
	AuditFields `gorm:"embedded"`
	Title       string
}

func newTypedProvider[T any](t *testing.T, keyOf func(*T) string) *Provider[T] {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	p := NewGormProvider[T](db, keyOf)
	require.NoError(t, p.Migrate(context.Background()))
	return p
}

// TestCustomColumnAndPrimaryKey 锁定：Update 用实体真实主键列定位，
// 过滤/排序按 Go 字段名解析 gorm:"column:..." 自定义列名。
func TestCustomColumnAndPrimaryKey(t *testing.T) {
	p := newTypedProvider[taggedUser](t, func(u *taggedUser) string { return u.UserID })
	ctx := context.Background()
	repo := store.NewStore[taggedUser](p)

	require.NoError(t, repo.Create(ctx, &taggedUser{UserID: "u1", Login: "alice", OrgID: "o1"}))
	require.NoError(t, repo.Create(ctx, &taggedUser{UserID: "u2", Login: "bob", OrgID: "o1"}))

	// 过滤字段按 Go 字段名 → 自定义列名（login_name）。
	got, err := repo.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("Login", "alice")}})
	require.NoError(t, err)
	assert.Equal(t, "u1", got.UserID)

	// 主键列是 uid：定位条件若仍写死 id 会直接报 no such column。
	rows, err := repo.Update(ctx, &taggedUser{UserID: "u1", Login: "alice2", OrgID: "o2", Ignore: "x"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)

	got, err = repo.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("UserID", "u1")}})
	require.NoError(t, err)
	assert.Equal(t, "alice2", got.Login)
	assert.Equal(t, "o2", got.OrgID)
	assert.Empty(t, got.Ignore)

	// 排序字段同样按自定义列名翻译。
	items, total, err := repo.List(ctx, &store.Where{
		Sorting: []*storev1.Sorting{{Field: "Login", Direction: storev1.Sorting_DESC}},
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, items, 2)
	assert.Equal(t, "bob", items[0].Login)
	assert.Equal(t, "alice2", items[1].Login)
}

// TestEmbeddedStructColumns 锁定：嵌入结构体的字段按列展开写入与查询，
// 不会被当作单个列（旧实现会生成 "audit_fields" 列导致 SQL 错误）。
func TestEmbeddedStructColumns(t *testing.T) {
	p := newTypedProvider[embeddedDoc](t, func(d *embeddedDoc) string { return d.ID })
	ctx := context.Background()
	repo := store.NewStore[embeddedDoc](p)

	require.NoError(t, repo.Create(ctx, &embeddedDoc{ID: "d1", AuditFields: AuditFields{CreatedBy: "alice"}, Title: "t1"}))

	rows, err := repo.Update(ctx, &embeddedDoc{ID: "d1", AuditFields: AuditFields{CreatedBy: "bob"}, Title: "t2"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)

	got, err := repo.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("CreatedBy", "bob")}})
	require.NoError(t, err)
	assert.Equal(t, "d1", got.ID)
	assert.Equal(t, "t2", got.Title)
	assert.Equal(t, "bob", got.CreatedBy)
}

// TestModelMetaPrimaryKeyFallback 锁定：无 primaryKey tag 时回落到 ID 字段，
// 完全没有主键信息时仍按 id 列定位（保持旧行为）。
func TestModelMetaPrimaryKeyFallback(t *testing.T) {
	if got := modelMetaOf[User]().primaryKey; got != "id" {
		t.Fatalf("User primaryKey = %q, want id", got)
	}

	type noKey struct {
		Name string
	}
	if got := modelMetaOf[noKey]().primaryKey; got != "id" {
		t.Fatalf("noKey primaryKey = %q, want fallback id", got)
	}

	type customIDColumn struct {
		ID string `gorm:"column:uid"`
	}
	if got := modelMetaOf[customIDColumn]().primaryKey; got != "uid" {
		t.Fatalf("customIDColumn primaryKey = %q, want uid", got)
	}
}

// compositeDoc 使用 (org_id, seq) 复合主键。
type compositeDoc struct {
	OrgID string `gorm:"column:org_id;primaryKey"`
	Seq   int    `gorm:"primaryKey"`
	Name  string
}

// TestCompositePrimaryKeyUpdate 锁定：复合主键按全部主键列定位，
// 只命中一行，且主键列不会被写入更新字段。
func TestCompositePrimaryKeyUpdate(t *testing.T) {
	// 复合主键下 keyOf 不参与定位，任取一个值即可。
	p := newTypedProvider[compositeDoc](t, func(d *compositeDoc) string { return d.OrgID })
	ctx := context.Background()
	repo := store.NewStore[compositeDoc](p)

	require.NoError(t, repo.Create(ctx, &compositeDoc{OrgID: "o1", Seq: 1, Name: "a"}))
	require.NoError(t, repo.Create(ctx, &compositeDoc{OrgID: "o1", Seq: 2, Name: "b"}))
	require.NoError(t, repo.Create(ctx, &compositeDoc{OrgID: "o2", Seq: 1, Name: "c"}))

	rows, err := repo.Update(ctx, &compositeDoc{OrgID: "o1", Seq: 1, Name: "a2"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)

	// 主键列不得进入更新字段。
	updateMap := toUpdateMap(&compositeDoc{OrgID: "o1", Seq: 1, Name: "a2"}, modelMetaOf[compositeDoc]())
	assert.NotContains(t, updateMap, "org_id")
	assert.NotContains(t, updateMap, "seq")

	pick := func(org string) map[int]*compositeDoc {
		t.Helper()
		items, _, err := repo.List(ctx, &store.Where{Filters: []*storev1.FilterCondition{
			store.Eq("OrgID", org),
		}})
		require.NoError(t, err)
		bySeq := make(map[int]*compositeDoc, len(items))
		for _, item := range items {
			bySeq[item.Seq] = item
		}
		return bySeq
	}
	o1 := pick("o1")
	require.Len(t, o1, 2)
	assert.Equal(t, "a2", o1[1].Name)
	assert.Equal(t, "b", o1[2].Name)
	o2 := pick("o2")
	require.Len(t, o2, 1)
	assert.Equal(t, "c", o2[1].Name)
}
