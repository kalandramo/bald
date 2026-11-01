package asynq

import (
	"context"
	"sync"
	"testing"
	"time"
)

// newIdleServer 构造不连接 Redis 的服务器（三个组件全部禁用），
// 用于验证生命周期本身，不需外部依赖。
func newIdleServer(opts ...Option) *Server {
	base := []Option{
		WithServerEnabled(false),
		WithClientEnabled(false),
		WithSchedulerEnabled(false),
	}
	return NewServer(append(base, opts...)...)
}

// TestStartWithCanceledContextReturns 锁定：ctx 已取消时 Start 立即返回，
// 不会因为停机路径无界等待而挂住。
func TestStartWithCanceledContextReturns(t *testing.T) {
	srv := newIdleServer()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after ctx cancellation")
	}
}

// TestStopBeforeStartIsPrompt 锁定：未启动时 Stop 立即返回，且可重复调用。
func TestStopBeforeStartIsPrompt(t *testing.T) {
	srv := newIdleServer()

	for i := 0; i < 2; i++ {
		start := time.Now()
		if err := srv.Stop(context.Background()); err != nil {
			t.Fatalf("Stop #%d: %v", i+1, err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("Stop #%d took %v", i+1, elapsed)
		}
	}
}

// TestStopConcurrentCalls 锁定：Start 触发的停机与显式 Stop 并发时，
// 实际停机只执行一次，两个调用方都能返回。
func TestStopConcurrentCalls(t *testing.T) {
	srv := newIdleServer()

	ctx, cancel := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() { startDone <- srv.Start(ctx) }()

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer stopCancel()
			if err := srv.Stop(stopCtx); err != nil {
				t.Errorf("Stop: %v", err)
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	wg.Wait()

	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return")
	}
}

// TestShutdownTimeoutResolution 锁定：Start 触发的停机窗口优先取
// asynq.Config.ShutdownTimeout，未配置时回落到默认值。
func TestShutdownTimeoutResolution(t *testing.T) {
	if got := newIdleServer().shutdownTimeout(); got != defaultShutdownTimeout {
		t.Fatalf("shutdownTimeout() = %v, want %v", got, defaultShutdownTimeout)
	}

	srv := newIdleServer(WithShutdownTimeout(3 * time.Second))
	if got := srv.shutdownTimeout(); got != 3*time.Second {
		t.Fatalf("shutdownTimeout() = %v, want 3s", got)
	}
}
