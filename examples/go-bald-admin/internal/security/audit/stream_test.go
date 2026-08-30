package audit

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/kalandramo/bald/pkg/audit"

	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
)

// TestStreamAuditor_Record_PublishesToRedis 用真实 miniredis 验证事件异步 XADD 到 stream。
func TestStreamAuditor_Record_PublishesToRedis(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	a := NewStream(rdb)
	defer a.Stop()

	ev := audit.AuditEvent{
		Time:    time.Now(),
		Subject: "u-admin",
		Object:  "secret",
		Action:  "get",
		Result:  audit.ResultAllow,
	}
	a.Record(context.Background(), ev)

	// 后台 goroutine 异步 XADD：轮询等待 stream 有消息（带超时）。
	deadline := time.Now().Add(2 * time.Second)
	for {
		n, err := rdb.XLen(context.Background(), "audit.events").Result()
		if err == nil && n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream has no message after 2s: len=%d err=%v", n, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 取首条消息，校验载荷含 subject/object。
	msgs, err := rdb.XRange(context.Background(), "audit.events", "-", "+").Result()
	if err != nil {
		t.Fatalf("xrange: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("no message")
	}
	raw, ok := msgs[0].Values["event"].(string)
	if !ok {
		t.Fatalf("event field missing: %v", msgs[0].Values)
	}
	var got audit.AuditEvent
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Subject != "u-admin" || got.Object != "secret" {
		t.Fatalf("payload mismatch: %+v", got)
	}
}

// TestStreamAuditor_NilRedis_ReturnsNil NewStream(nil) 应返 nil（调用方跳过流后端）。
func TestStreamAuditor_NilRedis_ReturnsNil(t *testing.T) {
	if NewStream(nil) != nil {
		t.Fatal("NewStream(nil) should return nil")
	}
}

// TestMultiAuditor_CombinesStoreAndStream 组合落库 + 入流，两后端均收到同一事件。
func TestMultiAuditor_CombinesStoreAndStream(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "audit_test.db")
	db, err := gorm.Open(sqlite.Open(dbFile), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&authmodel.AuditRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	m := NewMulti(NewStore(db), NewStream(rdb))
	defer func() {
		if s, ok := m.auditors[1].(*StreamAuditor); ok {
			s.Stop()
		}
	}()

	ev := audit.AuditEvent{Time: time.Now(), Subject: "u-alice", Object: "secret", Action: "delete", Result: audit.ResultDeny, Error: "denied"}
	m.Record(context.Background(), ev)

	// 落库断言。
	var recs []authmodel.AuditRecord
	if err := db.Find(&recs).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(recs) != 1 || recs[0].Subject != "u-alice" {
		t.Fatalf("store got %d recs: %+v", len(recs), recs)
	}

	// 入流断言（异步，轮询）。
	deadline := time.Now().Add(2 * time.Second)
	for {
		n, _ := rdb.XLen(context.Background(), "audit.events").Result()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stream has no message after 2s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
