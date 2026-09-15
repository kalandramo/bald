package audit

import (
	"context"
	"testing"
)

// TestMultiAuditor_BroadcastOrder 契约：事件按构造序广播到全部后端。
func TestMultiAuditor_BroadcastOrder(t *testing.T) {
	first, second := &memAuditor{}, &memAuditor{}
	m := MultiAuditor{first, second}

	ev := AuditEvent{Subject: "u1", Object: "secret", Action: "get", Result: ResultAllow}
	m.Record(context.Background(), ev)

	for i, a := range []*memAuditor{first, second} {
		got := a.all()
		if len(got) != 1 || got[0].Subject != "u1" || got[0].Object != "secret" || got[0].Result != ResultAllow {
			t.Fatalf("auditor[%d] events = %+v, want 1x u1/secret/allow", i, got)
		}
	}
}

// TestMultiAuditor_NilMembersSkipped 契约：nil 成员跳过，不 panic。
func TestMultiAuditor_NilMembersSkipped(t *testing.T) {
	sink := &memAuditor{}
	m := MultiAuditor{nil, sink, nil}

	m.Record(context.Background(), AuditEvent{Subject: "u2"})

	if got := sink.all(); len(got) != 1 || got[0].Subject != "u2" {
		t.Fatalf("events = %+v, want 1 event from u2", got)
	}
}

// TestNewMultiAuditor_EmptyReturnsNop 契约：空/全 nil 入参返回 Nop（零副作用）。
func TestNewMultiAuditor_EmptyReturnsNop(t *testing.T) {
	if _, ok := NewMultiAuditor().(nopAuditor); !ok {
		t.Fatal("no args should return nopAuditor")
	}
	if _, ok := NewMultiAuditor(nil, nil).(nopAuditor); !ok {
		t.Fatal("all-nil args should return nopAuditor")
	}
}

// TestNewMultiAuditor_FiltersNil 契约：构造时过滤 nil 成员，非空保留。
func TestNewMultiAuditor_FiltersNil(t *testing.T) {
	sink := &memAuditor{}
	m, ok := NewMultiAuditor(nil, sink).(MultiAuditor)
	if !ok {
		t.Fatal("mixed args should return MultiAuditor")
	}
	if len(m) != 1 || m[0] != Auditor(sink) {
		t.Fatalf("members = %+v, want [sink]", m)
	}
}
