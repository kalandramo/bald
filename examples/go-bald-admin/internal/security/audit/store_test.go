package audit

import (
	"context"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/kalandramo/bald/pkg/audit"

	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
)

// newTestDB 建内存 SQLite 并迁移审计表（真实 GORM + DAL，符合 §0）。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&authmodel.AuditRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestStoreAuditor_Record_Persists 落库后端应把事件写入 audit_records 表。
func TestStoreAuditor_Record_Persists(t *testing.T) {
	db := newTestDB(t)
	a := NewStore(db)

	ev := audit.AuditEvent{
		Time:     time.Now(),
		Subject:  "u-admin",
		TenantID: "t-acme",
		Object:   "secret",
		Action:   "get",
		Result:   "allow",
	}
	a.Record(context.Background(), ev)

	var recs []authmodel.AuditRecord
	if err := db.Find(&recs).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs))
	}
	got := recs[0]
	if got.Subject != "u-admin" || got.TenantID != "t-acme" || got.Object != "secret" ||
		got.Action != "get" || got.Result != "allow" {
		t.Fatalf("persisted record mismatch: %+v", got)
	}
}

// TestStoreAuditor_DBError_FallsBack 模拟落库失败时降级到 fallback（LoggerAuditor 不 panic）。
func TestStoreAuditor_DBError_FallsBack(t *testing.T) {
	// 用 nil DB：NewStore 默认 fallback=LoggerAuditor，Record 应静默调用 fallback 而不 panic。
	a := NewStore(nil)
	ev := audit.AuditEvent{
		Time:    time.Now(),
		Subject: "u-alice",
		Object:  "secret",
		Action:  "delete",
		Result:  "deny",
		Error:   "permission denied",
	}
	// 不应 panic；fallback 记日志（测试环境无副作用断言，仅验证不崩）。
	a.Record(context.Background(), ev)
}
