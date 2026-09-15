package contract

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/audit"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// auditEvent 构造最小审计事件。
func auditEvent(subject string) audit.AuditEvent {
	return audit.AuditEvent{Subject: subject, Object: "secret", Action: "get", Result: audit.ResultAllow}
}

// newDB 内存 sqlite。
func newDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// TestNewStoreProvider_Migrate 契约：store.migrate=true 自动迁移默认表，
// 装配产物可落库。
func TestNewStoreProvider_Migrate(t *testing.T) {
	db := newDB(t)
	p := NewStoreProvider(db)

	a, cleanup, err := p(context.Background(), &bootstrapv1.Audit{
		Type:  "store",
		Store: &bootstrapv1.Audit_Store{Migrate: true},
	})
	if err != nil || a == nil || cleanup != nil {
		t.Fatalf("build: a=%v cleanup-set=%v err=%v", a, cleanup != nil, err)
	}

	// 迁移已生效：Record 落库成功。
	a.Record(context.Background(), auditEvent("u1"))
	var n int64
	if err := db.Table("audit_records").Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

// TestNewStoreProvider_NoMigrateByDefault 契约：migrate 缺省 false，不迁移
// （未迁移表上落库失败走降级，不向上游报错）。
func TestNewStoreProvider_NoMigrateByDefault(t *testing.T) {
	db := newDB(t)
	p := NewStoreProvider(db)

	a, _, err := p(context.Background(), &bootstrapv1.Audit{Type: "store"})
	if err != nil || a == nil {
		t.Fatalf("build: a=%v err=%v", a, err)
	}

	// 表未迁移：Create 报错，但 Record 旁路不 panic。
	a.Record(context.Background(), auditEvent("u2"))
}
