package audit

import (
	"context"
	"sync"
	"testing"
)

// memAuditor 是测试用内存 Auditor，记录所有事件供断言。
type memAuditor struct {
	mu     sync.Mutex
	events []AuditEvent
}

func (m *memAuditor) Record(_ context.Context, e AuditEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
}

func (m *memAuditor) all() []AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AuditEvent, len(m.events))
	copy(out, m.events)
	return out
}

func TestNopAuditor_RecordNoop(t *testing.T) {
	// nop 不应 panic，也不应产生副作用。
	NopAuditor().Record(context.Background(), AuditEvent{Subject: "u1"})
}

func TestGlobalSetGet(t *testing.T) {
	a := &memAuditor{}
	SetAuditor(a)
	defer SetAuditor(nil)
	if GetAuditor() != a {
		t.Fatal("GetAuditor should return the injected auditor")
	}
	a.Record(context.Background(), AuditEvent{Subject: "u2", Result: ResultAllow})
	if got := a.all(); len(got) != 1 || got[0].Subject != "u2" {
		t.Fatalf("unexpected events: %+v", got)
	}
}

func TestSetAuditor_NilFallsBackToNop(t *testing.T) {
	SetAuditor(nil)
	if _, ok := GetAuditor().(nopAuditor); !ok {
		t.Fatal("nil should reset to nopAuditor")
	}
}
