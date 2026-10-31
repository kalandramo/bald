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

// TestAssemble_UserOnConfigChangeFires 钉住装配路径的全量热更回调：
// assemble 重建 kitOpts 时若漏掉 spec.OnConfigChange，用户的 OnConfigChange
// 会静默失效（Serve → Compile → assemble 是生产唯一入口，直接 NewApp 的
// 用例覆盖不到）。本测试从文件 watch 端到端验证回调确实被调用。
func TestAssemble_UserOnConfigChangeFires(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	path := filepath.Join(t.TempDir(), "onchange.yaml")
	writeConfig := func(name string) {
		t.Helper()
		doc := "app:\n  name: \"" + name + "\"\n"
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	writeConfig("onchange-a")

	var calls int32
	a := assembleForTest(t, bconf.NewBootstrap(),
		ConfigFile(path),
		WatchConfigFile(true),
		OnConfigChange(func(map[string]any) { atomic.AddInt32(&calls, 1) }),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not return")
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if d, err := a.Drift(context.Background()); err == nil && len(d) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial drift not settled")
		}
		time.Sleep(5 * time.Millisecond)
	}

	time.Sleep(20 * time.Millisecond)
	writeConfig("onchange-b")
	deadline = time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&calls) < 1 {
		if time.Now().After(deadline) {
			t.Fatal("user OnConfigChange was not invoked on config change")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
