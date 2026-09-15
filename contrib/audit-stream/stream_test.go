package auditstream

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kalandramo/bald/pkg/audit"
)

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

// newTestRedis 启动 miniredis 并返回客户端。
func newTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

// waitFor 轮询等待条件成立（超时 2s 失败）。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

// TestStreamAuditor_PublishesToStream 契约：事件经后台 XADD 发布到 stream（JSON 载荷）。
func TestStreamAuditor_PublishesToStream(t *testing.T) {
	_, rdb := newTestRedis(t)
	a := New(rdb)
	if a == nil {
		t.Fatal("New returned nil")
	}

	ev := audit.AuditEvent{
		Subject: "u1", TenantID: "t1", Object: "secret", Action: "get",
		Result: audit.ResultAllow, Meta: map[string]any{"request_id": "req-1"},
	}
	a.Record(context.Background(), ev)

	waitFor(t, func() bool {
		n, _ := rdb.XLen(context.Background(), "audit.events").Result()
		return n == 1
	})

	msgs, err := rdb.XRange(context.Background(), "audit.events", "-", "+").Result()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("xrange: %v, msgs=%d", err, len(msgs))
	}
	var got audit.AuditEvent
	if err := json.Unmarshal([]byte(msgs[0].Values["event"].(string)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Subject != "u1" || got.Object != "secret" || got.Action != "get" || got.Result != audit.ResultAllow {
		t.Fatalf("event mismatch: %+v", got)
	}
}

// TestStreamAuditor_BufferFullFallsBack 契约：缓冲满时 Record 降级 fallback（不阻塞、不丢事件）。
func TestStreamAuditor_BufferFullFallsBack(t *testing.T) {
	_, rdb := newTestRedis(t)
	fb := &memAuditor{}
	// buffer=1 + 先 Close（后台退出、drain 空缓冲）→ 后续 Record 只入队不消费。
	a := New(rdb, WithBuffer(1), WithFallback(fb))
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	a.Record(context.Background(), audit.AuditEvent{Subject: "e1"}) // 入队（占满）
	a.Record(context.Background(), audit.AuditEvent{Subject: "e2"}) // 满→fallback

	if got := fb.all(); len(got) != 1 || got[0].Subject != "e2" {
		t.Fatalf("fallback events = %+v, want 1x e2", got)
	}
}

// TestStreamAuditor_CloseFlushesBuffer 契约：Close drain 剩余缓冲，已入队事件不丢弃。
func TestStreamAuditor_CloseFlushesBuffer(t *testing.T) {
	_, rdb := newTestRedis(t)
	fb := &memAuditor{}
	a := New(rdb, WithFallback(fb))

	for _, s := range []string{"a", "b", "c"} {
		a.Record(context.Background(), audit.AuditEvent{Subject: s})
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 后台消费 + Close drain 的总量应等于入队量（不丢不重）。
	n, err := rdb.XLen(context.Background(), "audit.events").Result()
	if err != nil {
		t.Fatalf("xlen: %v", err)
	}
	if n != 3 {
		t.Fatalf("stream length = %d, want 3 (fallback got %d)", n, len(fb.all()))
	}
}

// TestStreamAuditor_CloseIdempotent 契约：重复 Close 不 panic。
func TestStreamAuditor_CloseIdempotent(t *testing.T) {
	_, rdb := newTestRedis(t)
	a := New(rdb)
	if err := a.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// TestStreamAuditor_PublishFailureFallsBack 契约：Redis 不可达时发布失败降级 fallback。
func TestStreamAuditor_PublishFailureFallsBack(t *testing.T) {
	mr, rdb := newTestRedis(t)
	fb := &memAuditor{}
	a := New(rdb, WithFallback(fb))

	mr.Close() // 模拟 Redis 故障

	a.Record(context.Background(), audit.AuditEvent{Subject: "u9"})
	waitFor(t, func() bool { return len(fb.all()) == 1 })

	if got := fb.all(); len(got) != 1 || got[0].Subject != "u9" {
		t.Fatalf("fallback events = %+v, want 1x u9", got)
	}
}

// TestNew_NilClientReturnsNil 契约：nil 客户端返回 nil（调用方跳过装配）。
func TestNew_NilClientReturnsNil(t *testing.T) {
	if a := New(nil); a != nil {
		t.Fatalf("New(nil) = %v, want nil", a)
	}
}
