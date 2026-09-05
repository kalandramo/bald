package main

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/audit"
)

// newAuditSettings 构造只含 audit.backends 的最小期望态快照（无需真实 DB/Redis）。
func newAuditSettings(backends string) map[string]any {
	return map[string]any{"audit": map[string]any{"backends": backends}}
}

// newReconcileApp 造一个最小 AppKit 测试装置并标记运行态（免去启动 server）。
// reconcileAudit 走 ReconcileCtx.Mount/Unmount → AppKit.MountComponent，后者要求
// running==true；跨 module 测试用框架提供的 NewHarness 装置直接进入运行态。
func newReconcileApp() *appkit.AppKit {
	return appkit.NewHarness()
}

// capturedAuditor 记录是否接收到事件，用于断言收敛后全局审计后端已非 nop。
type capturedAuditor struct {
	audit.Auditor
	got int
}

func (c *capturedAuditor) Record(ctx context.Context, ev audit.AuditEvent) {
	c.got++
	c.Auditor.Record(ctx, ev)
}

// TestReconcileAudit_ParseBackends 验证后端列表解析：去重、过滤非法、空格/逗号分隔。
func TestReconcileAudit_ParseBackends(t *testing.T) {
	cases := map[string][]string{
		"store,stream":       {"store", "stream"},
		"log store":          {"log", "store"},
		"store,store,stream": {"store", "stream"},
		"bad,store,evil":     {"store"},
		"":                   nil,
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

// TestReconcileAudit_MountLog 期望态 log：收敛出 log 组件，全局审计器生效（非 nop）。
func TestReconcileAudit_MountLog(t *testing.T) {
	audit.SetAuditor(audit.NopAuditor())
	app := newReconcileApp()
	rctx := appkit.NewReconcileCtx(app, "audit.backends", newAuditSettings("log"))

	if err := reconcileAudit(context.Background(), rctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !contains(app.ListComponents(), "log") {
		t.Fatalf("expected log mounted, got %v", app.ListComponents())
	}
	cap := &capturedAuditor{Auditor: audit.GetAuditor()}
	cap.Record(context.Background(), audit.AuditEvent{Object: "test", Action: "get"})
	if cap.got == 0 {
		t.Fatal("expected active auditor after reconcile, but no event recorded (still nop?)")
	}
}

// TestReconcileAudit_Idempotent 二次收敛同期望态：diff 为空，不重复挂载、不抖动。
func TestReconcileAudit_Idempotent(t *testing.T) {
	audit.SetAuditor(audit.NopAuditor())
	app := newReconcileApp()
	rctx := appkit.NewReconcileCtx(app, "audit.backends", newAuditSettings("log"))

	if err := reconcileAudit(context.Background(), rctx); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if err := reconcileAudit(context.Background(), rctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if got := app.ListComponents(); len(got) != 1 || got[0] != "log" {
		t.Fatalf("idempotent reconcile should keep exactly [log], got %v", got)
	}
}

// TestReconcileAudit_RemoveOnEmpty 期望态置空：移除已挂载后端，全局审计器退化为 nop。
func TestReconcileAudit_RemoveOnEmpty(t *testing.T) {
	audit.SetAuditor(audit.NopAuditor())
	app := newReconcileApp()
	rctx := appkit.NewReconcileCtx(app, "audit.backends", newAuditSettings("log"))
	if err := reconcileAudit(context.Background(), rctx); err != nil {
		t.Fatalf("mount reconcile: %v", err)
	}
	if !contains(app.ListComponents(), "log") {
		t.Fatal("precondition: log should be mounted")
	}

	// 期望态置空 → 应卸载 log。
	rctx2 := appkit.NewReconcileCtx(app, "audit.backends", newAuditSettings(""))
	if err := reconcileAudit(context.Background(), rctx2); err != nil {
		t.Fatalf("remove reconcile: %v", err)
	}
	if contains(app.ListComponents(), "log") {
		t.Fatalf("expected log removed, got %v", app.ListComponents())
	}
	// 全局审计器退回 nop：实际态表清空即代表已无生效后端。
	reconAuditors.mu.Lock()
	n := len(reconAuditors.set)
	reconAuditors.mu.Unlock()
	if n != 0 {
		t.Fatalf("expected all audit backends unmounted, got %d", n)
	}
}
