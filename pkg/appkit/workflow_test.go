package appkit

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
	"github.com/kalandramo/bald/log"
)

// FromBootstrap 契约装配：阶段 B 构建 → Workflow 取用 → 停机 cleanup。
// Registry 行为测试（注册/段枚举/回滚）已随六域 Registry 迁 bootstrap
// （bootstrap/workflow_test.go，2026-09-15）。

func TestFromBootstrap_ContractWorkflowLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	type stubClient struct{ name string }
	cleaned := false
	wr := baldbootstrap.NewWorkflowRegistry()
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
