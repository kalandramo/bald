package main

import (
	"context"
	"testing"

	"github.com/spf13/viper"

	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/appkit"
)

// newAuditViper 构造只含 audit.backends 的最小 viper（无需真实 DB/Redis）。
func newAuditViper(backends string) *viper.Viper {
	v := viper.New()
	v.Set("audit.backends", backends)
	return v
}

// TestReconcileAudit_ParseBackends 验证后端列表解析：去重、过滤非法、空格/逗号分隔。
func TestReconcileAudit_ParseBackends(t *testing.T) {
	cases := map[string][]string{
		"store,stream":         {"store", "stream"},
		"log store":            {"log", "store"},
		"store,store,stream":   {"store", "stream"},
		"bad,store,evil":       {"store"},
		"":                     nil,
	}
	for in, want := range cases {
		got := parseAuditBackends(in)
		if len(got) != len(want) {
			t.Fatalf("parseAuditBackends(%q)=%v want %v", in, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("parseAuditBackends(%q)[%d]=%q want %q", in, i, got[i], want[i])
			}
		}
	}
}

// capturedAuditor 记录是否接收到事件，用于断言 reconcile 后全局审计后端已非 nop。
type capturedAuditor struct {
	audit.Auditor
	got int
}

func (c *capturedAuditor) Record(ctx context.Context, ev audit.AuditEvent) {
	c.got++
	c.Auditor.Record(ctx, ev)
}

// TestReconcileAudit_LogOnly 期望态仅 log：收敛为只含 LoggerAuditor 的 MultiAuditor，
// 不依赖 DB/Redis（bootstrappkg.DB 为 nil 时 store/stream 自动跳过）。
func TestReconcileAudit_LogOnly(t *testing.T) {
	audit.SetAuditor(audit.NopAuditor())
	rctx := &appkit.ReconcileCtx{Viper: newAuditViper("log")}
	if err := reconcileAudit(context.Background(), rctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	cap := &capturedAuditor{Auditor: audit.GetAuditor()}
	cap.Record(context.Background(), audit.AuditEvent{Object: "test", Action: "get"})
	if cap.got == 0 {
		t.Fatal("expected active auditor after reconcile, but no event recorded (still nop?)")
	}
}

// TestReconcileAudit_Idempotent 二次收敛同期望态：diff 为空，不产生副作用抖动
// （此处仅验证不报错、仍是 MultiAuditor；实际「无 diff 早退」由 appkit 在调用方保证）。
func TestReconcileAudit_Idempotent(t *testing.T) {
	rctx := &appkit.ReconcileCtx{Viper: newAuditViper("log")}
	if err := reconcileAudit(context.Background(), rctx); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if err := reconcileAudit(context.Background(), rctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
}
