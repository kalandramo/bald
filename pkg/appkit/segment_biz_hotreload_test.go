package appkit

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	"github.com/kalandramo/bald/log"
)

// TestHotReload_ReconcilesBizSegment 钉住 Q8 的端到端链路：业务配置（biz 段）
// 变更 → 计划重算（biz.<path> 期望态摘要变化）→ Domain.Reconcile 被调用 →
// Drift 收敛为空。与 provider 节点共用同一条 diff/协调路径。
func TestHotReload_ReconcilesBizSegment(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	path := filepath.Join(t.TempDir(), "biz-reload.yaml")
	writeConfig := func(policy string) {
		t.Helper()
		doc := "app:\n  name: \"biz-reload\"\nbiz:\n  audit:\n    policy: \"" + policy + "\"\n"
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	writeConfig("a")

	var calls int32
	dom := bizDomain("audit", "biz.audit")
	dom.Reconcile = func(context.Context, *App, Node) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	a := assembleForTest(t, bconf.NewBootstrap(),
		Name("biz-reload"),
		ConfigFile(path),
		WatchConfigFile(true),
		dom,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return")
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		d, err := a.Drift(context.Background())
		if err == nil && len(d) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("initial drift = %+v, err=%v; want none", d, err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	time.Sleep(20 * time.Millisecond)
	writeConfig("b")
	deadline = time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&calls) < 1 {
		if time.Now().After(deadline) {
			t.Fatal("biz config change did not trigger Domain.Reconcile")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if d, err := a.Drift(context.Background()); err != nil || len(d) != 0 {
		t.Fatalf("drift after biz reload = %+v, err=%v; want none", d, err)
	}
}
