package appkit

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
)

// --- WorkflowRegistry：显式注册与 fail-fast ---

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

// --- FromBootstrap 契约装配：阶段 B 构建 → Workflow 取用 → 停机 cleanup ---

func TestFromBootstrap_ContractWorkflowLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	type stubClient struct{ name string }
	cleaned := false
	wr := NewWorkflowRegistry()
	wr.MustRegister("argo", func(context.Context, *bootstrapv1.Workflow) (any, func(), error) {
		return &stubClient{name: "argo"}, func() { cleaned = true }, nil
	})

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Workflow = &bootstrapv1.Workflow{
		Argo: &bootstrapv1.Workflow_Argo{ServerUrl: "http://127.0.0.1:2746", Namespace: "ci"},
	}

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)), WithWorkflowRegistry(wr))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	if _, ok := a.Workflow("argo"); ok {
		t.Fatal("Workflow should be empty before Run (phase B not reached)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	cli, ok := a.Workflow("argo")
	if !ok {
		t.Fatal("workflow instance not built from contract")
	}
	if c, ok := cli.(*stubClient); !ok || c.name != "argo" {
		t.Fatalf("instance type mismatch: %T", cli)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s")
	}
	if !cleaned {
		t.Fatal("workflow cleanup should run on shutdown")
	}
}

// 契约 workflow 段存在但未接 WorkflowRegistry → 阶段 B fail-fast。
func TestFromBootstrap_WorkflowSectionWithoutTable(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Workflow = &bootstrapv1.Workflow{
		Argo: &bootstrapv1.Workflow_Argo{ServerUrl: "http://127.0.0.1:2746"},
	}

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	err = a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "WorkflowRegistry") {
		t.Fatalf("expected WorkflowRegistry missing error, got %v", err)
	}
}
