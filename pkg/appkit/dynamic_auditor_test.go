package appkit

import (
	"context"
	"sync"
	"testing"

	"github.com/kalandramo/bald/pkg/audit"
)

// recordingAuditor 记录型 mock auditor（测试用）。
type recordingAuditor struct {
	mu     sync.Mutex
	events []audit.AuditEvent
}

func (m *recordingAuditor) Record(_ context.Context, ev audit.AuditEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, ev)
}

func (m *recordingAuditor) snapshot() []audit.AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]audit.AuditEvent(nil), m.events...)
}

// TestDynamicAuditor_NoopOnZeroAndNew 零值与 New 构造后 Record 均为 no-op，不 panic。
func TestDynamicAuditor_NoopOnZeroAndNew(t *testing.T) {
	ev := audit.AuditEvent{Object: "auth", Action: "login"}

	var zero DynamicAuditor
	zero.Record(context.Background(), ev) // 零值不 panic

	d := NewDynamicAuditor()
	d.Record(context.Background(), ev) // 初始目标为 Nop，不 panic

	if got := d.Load(); got == nil {
		t.Fatal("Load must never return nil")
	}
}

// TestDynamicAuditor_StoreRedirectsRecord Store 一个记录型 auditor 后，Record 到达新目标。
func TestDynamicAuditor_StoreRedirectsRecord(t *testing.T) {
	m := &recordingAuditor{}
	d := NewDynamicAuditor()
	d.Store(m)

	d.Record(context.Background(), audit.AuditEvent{Object: "secret", Action: "get"})

	events := m.snapshot()
	if len(events) != 1 || events[0].Object != "secret" || events[0].Action != "get" {
		t.Fatalf("record did not reach stored auditor, got %+v", events)
	}
}

// TestDynamicAuditor_HotSwap 已持有代理引用的"中间件"在 Store 切换后，
// 下一次 Record 到达新目标——证明构造期快照代理即可热切换，无需重建中间件。
func TestDynamicAuditor_HotSwap(t *testing.T) {
	d := NewDynamicAuditor()
	// 模拟中间件在构造期快照的 auditor（持有的是代理引用，不是具体 sink）。
	var middlewareSink audit.Auditor = d

	first := &recordingAuditor{}
	d.Store(first)
	middlewareSink.Record(context.Background(), audit.AuditEvent{Object: "first"})

	second := &recordingAuditor{}
	d.Store(second)
	middlewareSink.Record(context.Background(), audit.AuditEvent{Object: "second"})

	if got := first.snapshot(); len(got) != 1 || got[0].Object != "first" {
		t.Fatalf("first auditor events = %+v", got)
	}
	if got := second.snapshot(); len(got) != 1 || got[0].Object != "second" {
		t.Fatalf("second auditor events = %+v", got)
	}
}

// TestDynamicAuditor_Transform WithAuditTransform 的变换被调用，且变换后的事件
// 才是转发给目标的内容。
func TestDynamicAuditor_Transform(t *testing.T) {
	calls := 0
	d := NewDynamicAuditor(WithAuditTransform(func(_ context.Context, ev audit.AuditEvent) audit.AuditEvent {
		calls++
		ev.Object = "transformed-" + ev.Object
		return ev
	}))
	m := &recordingAuditor{}
	d.Store(m)

	d.Record(context.Background(), audit.AuditEvent{Object: "auth"})

	if calls != 1 {
		t.Fatalf("transform called %d times, want 1", calls)
	}
	events := m.snapshot()
	if len(events) != 1 || events[0].Object != "transformed-auth" {
		t.Fatalf("target got unexpected events %+v", events)
	}
}

// TestDynamicAuditor_ImplementsAuditor 实现 audit.Auditor 接口。
func TestDynamicAuditor_ImplementsAuditor(t *testing.T) {
	var _ audit.Auditor = (*DynamicAuditor)(nil)
	var sink audit.Auditor = NewDynamicAuditor()
	sink.Record(context.Background(), audit.AuditEvent{}) // 接口调用不 panic
}

// TestInstallContractAuditor_RefreshesDynamicAuditor 契约 audit 段产物应刷新
// 已注入的 DynamicAuditor 目标：代理句柄存活（不被替换）、显式注入标记不变，
// 且后续 Record 落到新产物——这正是审计段热切换对已挂中间件生效的关键。
func TestInstallContractAuditor_RefreshesDynamicAuditor(t *testing.T) {
	d := NewDynamicAuditor()
	app := NewApp()
	app.setAuditor(d, false) // 显式注入代理

	product := &recordingAuditor{}
	app.installContractAuditor(product)

	if app.auditorValue() != d {
		t.Fatal("installContractAuditor must keep the DynamicAuditor handle, not replace it")
	}
	if app.auditorFromContract {
		t.Fatal("explicit injection flag must remain false when refreshing a DynamicAuditor")
	}

	d.Record(context.Background(), audit.AuditEvent{Object: "after-refresh"})
	if got := product.snapshot(); len(got) != 1 || got[0].Object != "after-refresh" {
		t.Fatalf("contract product did not receive record, got %+v", got)
	}
}
