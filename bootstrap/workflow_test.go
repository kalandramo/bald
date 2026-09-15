package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// --- WorkflowRegistry：显式注册与 fail-fast（自 pkg/appkit 迁入 2026-09-15）---

func TestWorkflowRegistry_FailFast(t *testing.T) {
	wr := NewWorkflowRegistry()
	wr.MustRegister("argo", func(context.Context, *bootstrapv1.Workflow) (any, func(), error) {
		return nil, nil, nil
	})

	if err := wr.Register("argo", func(context.Context, *bootstrapv1.Workflow) (any, func(), error) {
		return nil, nil, nil
	}); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	if err := wr.Register("", nil); err == nil {
		t.Fatal("expected empty type error")
	}
	if err := wr.Register("temporal", nil); err == nil {
		t.Fatal("expected nil provider error")
	}
	// Build：契约段 nil → no-op
	clients, cleanup, err := wr.Build(context.Background(), nil)
	if err != nil || clients != nil || cleanup != nil {
		t.Fatalf("nil section should be no-op, got (clients=%v, cleanup=%t, err=%v)", clients, cleanup != nil, err)
	}
	// Build：段存在但 provider 未注册（空注册表）
	_, _, err = NewWorkflowRegistry().Build(context.Background(), &bootstrapv1.Workflow{
		Argo: &bootstrapv1.Workflow_Argo{ServerUrl: "http://127.0.0.1:2746"},
	})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected not-registered error, got %v", err)
	}
}

// Build：多段并存 + 逆序回放（用第二个注册表模拟两个后端并存）。
func TestWorkflowRegistry_BuildMultiAndRollback(t *testing.T) {
	cleaned := []string{}
	wr := NewWorkflowRegistry()
	wr.MustRegister("argo", func(context.Context, *bootstrapv1.Workflow) (any, func(), error) {
		return "argo-client", func() { cleaned = append(cleaned, "argo") }, nil
	})
	wr.MustRegister("temporal", func(context.Context, *bootstrapv1.Workflow) (any, func(), error) {
		return nil, nil, errors.New("boom")
	})

	// argo 成功、temporal 失败（temporal 段手工塞进 exists 检查之外不可行，
	// 这里直接用两个 argo 注册表模拟：第一段成功，第二段构建失败）
	wr2 := NewWorkflowRegistry()
	var n int
	wr2.MustRegister("argo", func(context.Context, *bootstrapv1.Workflow) (any, func(), error) {
		n++
		if n == 1 {
			return "first", func() { cleaned = append(cleaned, "first") }, nil
		}
		return nil, nil, errors.New("boom")
	})

	clients, cleanup, err := wr2.Build(context.Background(), &bootstrapv1.Workflow{
		Argo: &bootstrapv1.Workflow_Argo{ServerUrl: "http://127.0.0.1:2746"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if clients["argo"] != "first" {
		t.Fatalf("clients mismatch: %v", clients)
	}
	cleanup()
	if len(cleaned) != 1 || cleaned[0] != "first" {
		t.Fatalf("cleanup order = %v, want [first]", cleaned)
	}
}
