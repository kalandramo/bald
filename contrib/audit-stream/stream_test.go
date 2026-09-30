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

// TestStreamAuditor_ZeroTimeBackfillsNow 契约：Time 零值兜底为记录时刻
// （AuditEvent「缺省时取记录时」），流载荷 JSON 的 Time 邻近 now，
// 不再出现 "0001-01-01T00:00:00Z"。
func TestStreamAuditor_ZeroTimeBackfillsNow(t *testing.T) {
	_, rdb := newTestRedis(t)
	a := New(rdb, WithFallback(nil))

	before := time.Now().Add(-time.Minute)
	a.Record(context.Background(), audit.AuditEvent{
		Subject: "u2", Object: "authn", Action: "authenticate", Result: audit.ResultDeny,
	})
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
	if got.Time.Before(before) || got.Time.After(time.Now().Add(time.Minute)) {
		t.Errorf("backfilled time = %v, want near now", got.Time)
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

// TestStreamAuditor_ZeroBufferKeepsDefault —— WithBuffer(0) 不使 stream 静默失效。
//
// 背景：契约段 audit.stream.buffer 省略时其值为 0，contract provider 会
// 无条件经 WithBuffer(int(cfg.GetBuffer())) 传入 0。此场景若造成缓冲为 0，
// 事件会一入队即"满"、全部降级到 fallback——stream 实际不工作却无报错
// （静默失效），正是「配置声明了却不生效」的陷阱。
//
// 实际保障来自 New 的缺省兜底（`if cfg.buffer <= 0 { cfg.buffer = 1024 }`），
// 而非 WithBuffer 自身。本测试锁定该行为，防止兜底被移除或挪位时静默回归。
// （注：本测试在加入时即为 GREEN——它锁的是既有正确行为，不是新修复。）
func TestStreamAuditor_ZeroBufferKeepsDefault(t *testing.T) {
	_, rdb := newTestRedis(t)
	fb := &memAuditor{}
	a := New(rdb, WithBuffer(0), WithFallback(fb))
	t.Cleanup(func() { _ = a.Close() })

	a.Record(context.Background(), audit.AuditEvent{Subject: "u-zb", Object: "zero-buffer"})

	// 事件应进 stream（缺省 1024 缓冲容纳得下），而非只走 fallback。
	waitXLen(t, rdb, "audit.events", 1)
	if len(fb.all()) != 0 {
		t.Fatalf("事件走了 fallback（%d 条）——缓冲被误设为 0", len(fb.all()))
	}
}

// waitXLen 轮询等待 Redis Stream 长度达标（后台 goroutine 异步发布）。
func waitXLen(t *testing.T, rdb *redis.Client, stream string, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n, err := rdb.XLen(context.Background(), stream).Result(); err == nil && n >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	n, _ := rdb.XLen(context.Background(), stream).Result()
	t.Fatalf("stream %q length = %d, want >= %d", stream, n, want)
}
