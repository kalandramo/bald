package auditgorm

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/kalandramo/bald/pkg/audit"
)

// newTestDB 内存 sqlite + 默认表迁移，测试结束关闭连接池。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(DefaultModel()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// memAuditor 测试桩：捕获降级事件。
type memAuditor struct {
	mu     sync.Mutex
	events []audit.AuditEvent
}

func (m *memAuditor) Record(_ context.Context, e audit.AuditEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
}

func (m *memAuditor) all() []audit.AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]audit.AuditEvent(nil), m.events...)
}

// TestStoreAuditor_PersistsEvent 契约：全字段落库（含 Meta 提取的扩展列）。
func TestStoreAuditor_PersistsEvent(t *testing.T) {
	db := newTestDB(t)
	a := New(db, WithFallback(nil)) // 关降级，纯落库路径

	ts := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	a.Record(context.Background(), audit.AuditEvent{
		Time:     ts,
		Subject:  "u1",
		TenantID: "t1",
		Object:   "secret",
		Action:   "delete",
		Result:   audit.ResultError,
		Error:    "boom",
		Meta: map[string]any{
			"category":   "login",
			"client_ip":  "1.2.3.4",
			"user_agent": "ua-x",
			"request_id": "req-9",
			"trace_id":   "trace-7",
		},
	})

	var rec AuditRecord
	if err := db.First(&rec).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if rec.Subject != "u1" || rec.TenantID != "t1" || rec.Object != "secret" ||
		rec.Action != "delete" || rec.Result != "error" || rec.Error != "boom" {
		t.Fatalf("core fields mismatch: %+v", rec)
	}
	if rec.Time != ts.UnixNano() {
		t.Errorf("time = %d, want %d", rec.Time, ts.UnixNano())
	}
	if rec.Category != "login" || rec.IPAddress != "1.2.3.4" || rec.UserAgent != "ua-x" ||
		rec.RequestID != "req-9" || rec.TraceID != "trace-7" {
		t.Errorf("meta-extracted fields mismatch: %+v", rec)
	}
}

// TestStoreAuditor_ZeroTimeBackfillsNow 契约：Time 零值兜底为记录时刻
// （AuditEvent「缺省时取记录时」），落库 Time 为邻近 now 的 UnixNano，
// 不再出现零值 UnixNano 负数（authn 失败/协调器/组件埋点路径不填 Time）。
func TestStoreAuditor_ZeroTimeBackfillsNow(t *testing.T) {
	db := newTestDB(t)
	New(db, WithFallback(nil)).Record(context.Background(), audit.AuditEvent{
		Subject: "u6", Object: "authn", Action: "authenticate", Result: audit.ResultDeny,
	})

	before := time.Now().Add(-time.Minute).UnixNano()
	after := time.Now().Add(time.Minute).UnixNano()
	var rec AuditRecord
	if err := db.First(&rec).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if rec.Time < before || rec.Time > after {
		t.Errorf("backfilled time = %d, want within [%d, %d]", rec.Time, before, after)
	}
}

// TestStoreAuditor_MetaDefaults 契约：Meta 缺键时扩展列兜底（Category=operation，其余空）。
func TestStoreAuditor_MetaDefaults(t *testing.T) {
	db := newTestDB(t)
	New(db).Record(context.Background(), audit.AuditEvent{
		Subject: "u2", Object: "auth", Action: "login", Result: audit.ResultAllow,
	})

	var rec AuditRecord
	if err := db.First(&rec).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if rec.Category != "operation" || rec.IPAddress != "" || rec.RequestID != "" {
		t.Fatalf("defaults mismatch: %+v", rec)
	}
}

// TestStoreAuditor_FallbackOnDBFailure 契约：落库失败（连接关闭）降级双写，不向上游报错。
func TestStoreAuditor_FallbackOnDBFailure(t *testing.T) {
	db := newTestDB(t)
	fb := &memAuditor{}
	a := New(db, WithFallback(fb))

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ev := audit.AuditEvent{Subject: "u3", Object: "secret", Action: "get", Result: audit.ResultDeny}
	a.Record(context.Background(), ev) // 不应 panic

	if got := fb.all(); len(got) != 1 || got[0].Subject != "u3" {
		t.Fatalf("fallback events = %+v, want 1x u3", got)
	}
}

// TestStoreAuditor_NilDBFallsBack 契约：nil db 全部走 fallback（旁路不 panic）。
func TestStoreAuditor_NilDBFallsBack(t *testing.T) {
	fb := &memAuditor{}
	a := New(nil, WithFallback(fb))

	a.Record(context.Background(), audit.AuditEvent{Subject: "u4"})

	if got := fb.all(); len(got) != 1 || got[0].Subject != "u4" {
		t.Fatalf("fallback events = %+v, want 1x u4", got)
	}
}

// TestStoreAuditor_WithRecordMapper 契约：自定义映射覆盖默认表记录构造。
func TestStoreAuditor_WithRecordMapper(t *testing.T) {
	db := newTestDB(t)

	// 业务自定义表：仅两列。
	type slimRecord struct {
		ID      uint `gorm:"primaryKey;autoIncrement"`
		Subject string
	}
	if err := db.AutoMigrate(&slimRecord{}); err != nil {
		t.Fatalf("migrate slim: %v", err)
	}

	a := New(db,
		WithRecordMapper(func(ev audit.AuditEvent) any {
			return &slimRecord{Subject: ev.Subject}
		}),
		WithFallback(nil),
	)
	a.Record(context.Background(), audit.AuditEvent{Subject: "u5", Object: "x"})

	var rec slimRecord
	if err := db.First(&rec).Error; err != nil {
		t.Fatalf("query slim: %v", err)
	}
	if rec.Subject != "u5" {
		t.Fatalf("subject = %q, want u5", rec.Subject)
	}
}

// TestDefaultModel 契约：默认模型可直接迁移。
func TestDefaultModel(t *testing.T) {
	db := newTestDB(t) // 内部已用 DefaultModel 迁移成功
	if m := DefaultModel(); m == nil {
		t.Fatal("DefaultModel returned nil")
	}
	_ = db // 仅验证迁移无错
}
