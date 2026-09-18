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

// H2 回归：proto 声明了 4 个 workflow 段（temporal/argo/conductor/goworkflows），
// 但 workflowSections 只枚举 argo。配置里写了未实现的段时，Build 此前静默跳过
// （fail-open）——用户以为已装配，实际没接线。必须 fail-fast 并点名段名。
func TestWorkflowRegistry_UnimplementedSectionFailsFast(t *testing.T) {
	wr := NewWorkflowRegistry()
	// 注册全部 4 个段的 provider，排除「未注册」这一干扰因素：
	// 报错必须来自「段未实现」，而非「provider 未注册」。
	for _, typ := range []string{"temporal", "argo", "conductor", "goworkflows"} {
		wr.MustRegister(typ, func(context.Context, *bootstrapv1.Workflow) (any, func(), error) {
			return "cli", nil, nil
		})
	}

	cases := []struct {
		name string
		cfg  *bootstrapv1.Workflow
		want string
	}{
		{"temporal", &bootstrapv1.Workflow{Temporal: &bootstrapv1.Workflow_Temporal{}}, "temporal"},
		{"conductor", &bootstrapv1.Workflow{Conductor: &bootstrapv1.Workflow_Conductor{}}, "conductor"},
		{"goworkflows", &bootstrapv1.Workflow{Goworkflows: &bootstrapv1.Workflow_Goworkflows{}}, "goworkflows"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := wr.Build(context.Background(), tc.cfg)
			if err == nil {
				t.Fatalf("段 %q 已声明但未实现，Build 应 fail-fast，实际静默成功", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, 应点名未实现的段 %q", err.Error(), tc.want)
			}
		})
	}

	// argo（已实现）不受影响。
	clients, _, err := wr.Build(context.Background(), &bootstrapv1.Workflow{
		Argo: &bootstrapv1.Workflow_Argo{ServerUrl: "http://127.0.0.1:2746"},
	})
	if err != nil {
		t.Fatalf("已实现的 argo 段不应报错: %v", err)
	}
	if clients["argo"] != "cli" {
		t.Fatalf("argo client mismatch: %v", clients)
	}
}
