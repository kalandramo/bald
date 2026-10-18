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
