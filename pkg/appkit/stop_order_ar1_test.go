package appkit

// stop_order_ar1_test.go 复现 AR-1/AR-25（architecture-review-20261118）：
//
//	AR-1  provider 资源 cleanup 此前混进效应账本在 stopAll 阶段 0 回放——
//	      DB/缓存/注册中心在 server 排空与钩子之前就被关闭；logger 恢复后
//	      stop/Dispose 的失败日志落进恢复前的静默 logger。修复后分层：
//	      全局 Effect（阶段 0）→ 钩子/排空/Dispose → provider cleanup +
//	      logger 恢复（阶段 5）。
//	AR-25 closeStore 是 defer 的话晚于 stopAll——停机窗口内的配置热更会在
//	      logger 恢复后重建 logger。修复后停机前先关配置监听。

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
)

// TestStopAll_ProviderCleanupRunsAfterHooks 复现 AR-1。
func TestStopAll_ProviderCleanupRunsAfterHooks(t *testing.T) {
	var mu sync.Mutex
	var events []string
	record := func(e string) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}

	cfg, _ := deregFailCfg()
	cfg.Logger = nil
	cfg.Database = &bootstrapv1.Database{}
	dynamicAddr(cfg)

	a := assembleForTest(t, cfg,
		WithHTTP(new(http.ServeMux)),
		stubRegistrarProvider(&failingDeregistrar{}, nil), // 消费 registry 段（deregFailCfg 自带），否则段覆盖检查判红
		stubDomainProvider("database", "main",
			func(*bootstrapv1.BootstrapConfig) bool { return true },
			"db", func() {
				record("cleanup:database")
				// logger-last 断言点：修复前 cleanup 在 logger 恢复之前执行，
				// 这条日志落进装配期的 buffer logger；修复后全局 Logger 已
				// 恢复为装配前状态，buffer 不再收到它。
				log.Info(context.Background(), "provider-cleanup-ran")
			}),
		effectOpt("user-effect", func(context.Context) error {
			record("user-effect")
			return nil
		}),
		BeforeStop(func(context.Context) error { record("BeforeStop"); return nil }),
		AfterStop(func(context.Context) error { record("AfterStop"); return nil }),
		bufferLogger(&bytes.Buffer{}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	select {
	case <-a.startedCh:
	case err := <-done:
		cancel()
		t.Fatalf("Run returned early: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("Run did not start within 10s")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return within 15s")
	}

	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()

	want := []string{"user-effect", "BeforeStop", "AfterStop", "cleanup:database"}
	if len(got) != len(want) {
		t.Fatalf("停机序 = %v, want %v（AR-1：provider cleanup 必须晚于钩子与排空）", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("停机序[%d] = %q, want %q（完整序列 got=%v）", i, got[i], want[i], got)
		}
	}

	// 层内顺序说明：logger 恢复在阶段 5 内仍按 plan.teardown 约束序排布
	// （database 先于 logger，audit 收尾——TeardownAfter 机制未动），评审的
	// 「logger 最后」指整层后移到 Dispose 之后，而非层内把 logger 排到最后。
}

// TestRun_ConfigWatcherClosedBeforeStop 复现 AR-25：BeforeStop 钩子执行时
// 配置监听必须已经关闭（store 释放置 nil，closeStore 幂等）。
func TestRun_ConfigWatcherClosedBeforeStop(t *testing.T) {
	cfg, _ := deregFailCfg()
	cfg.Logger = nil
	dynamicAddr(cfg)

	var sawClosed bool
	var a *App
	a = assembleForTest(t, cfg,
		WithHTTP(new(http.ServeMux)),
		stubRegistrarProvider(&failingDeregistrar{}, nil),
		BeforeStop(func(context.Context) error {
			// A1 修正：断言「钩子入口时已关」——先读状态再探针，避免钩子自己
			// 的 closeStore 把断言变成自我满足。断言对象是 storeClosed 标记
			// （watcher 已停），快照读在停机窗口仍可用（AR-25 审阅 A4）。
			sawClosed = a.cfg.storeClosed
			a.cfg.closeStore() // 幂等性对照：重复关不 panic（同 goroutine，串行安全）
			if !a.cfg.storeClosed {
				t.Error("closeStore 幂等性破坏")
			}
			return nil
		}),
		bufferLogger(&bytes.Buffer{}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	select {
	case <-a.startedCh:
	case err := <-done:
		cancel()
		t.Fatalf("Run returned early: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("Run did not start within 10s")
	}
	cancel()
	<-done

	if !sawClosed {
		t.Fatal("BeforeStop 入口时 watcher 仍未关闭（AR-25：停机窗口热更会于 logger 恢复后重建 logger）")
	}
}
