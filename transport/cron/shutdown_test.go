package cron

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingJob 返回一个会阻塞到 release 被调用的任务，用于模拟长任务。
func blockingJob(running chan<- struct{}, release <-chan struct{}) func() {
	var once sync.Once
	return func() {
		once.Do(func() { close(running) })
		<-release
	}
}

// startWithJob 启动带阻塞任务的服务器，并等待任务真正开始执行。
func startWithJob(t *testing.T, srv *Server) (cancel context.CancelFunc, release chan struct{}, done chan error) {
	t.Helper()

	running := make(chan struct{})
	release = make(chan struct{})
	if _, err := srv.NewTimerJob("@every 100ms", blockingJob(running, release)); err != nil {
		t.Fatalf("NewTimerJob: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()

	select {
	case <-running:
	case <-time.After(3 * time.Second):
		t.Fatal("job did not start in time")
	}
	return cancel, release, done
}

// TestStartShutdownBounded 锁定：Start 因 ctx 取消触发的停机受 shutdownTimeout
// 约束，运行中的长任务不会让进程退出无限挂起。
func TestStartShutdownBounded(t *testing.T) {
	srv := NewServer(WithShutdownTimeout(200 * time.Millisecond))
	cancel, release, done := startWithJob(t, srv)
	defer close(release)

	start := time.Now()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after failing to drain running job")
	}

	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("Start returned after %v, expected it to wait for the shutdown window", elapsed)
	}
}

// TestGracefullyShutdownDisabled 锁定：关闭优雅停机后不等待运行中的任务。
func TestGracefullyShutdownDisabled(t *testing.T) {
	srv := NewServer(
		WithGracefullyShutdown(false),
		WithShutdownTimeout(5*time.Second),
	)
	cancel, release, done := startWithJob(t, srv)
	defer close(release)

	start := time.Now()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start waited for running job despite gracefully shutdown disabled")
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Start took %v, expected a prompt return", elapsed)
	}
}

// TestStopHonorsContextWhileJobRunning 锁定：显式 Stop 不会因运行中的任务
// 忽略调用方 ctx 的截止时间。
func TestStopHonorsContextWhileJobRunning(t *testing.T) {
	srv := NewServer(WithShutdownTimeout(5 * time.Second))
	cancel, release, done := startWithJob(t, srv)
	defer close(release)
	defer func() {
		cancel()
		<-done
	}()

	ctx, stopCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stopCancel()

	start := time.Now()
	if err := srv.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Stop took %v, expected it to honor the ctx deadline", elapsed)
	}
}

// TestStopBeforeStartIsPrompt 锁定：未启动时 Stop 立即返回。
func TestStopBeforeStartIsPrompt(t *testing.T) {
	srv := NewServer()

	start := time.Now()
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Stop before Start took %v", elapsed)
	}

	// Start 之后再次 Stop 也应立即返回。
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	cancel()
	<-done

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// TestRestartAfterStop 锁定：Stop 后再次 Start，新一轮调度仍可运行，
// 且第二次停机真的会停下任务（停机完成标记按轮重置）。
func TestRestartAfterStop(t *testing.T) {
	srv := NewServer(WithGracefullyShutdown(false))

	var runs atomic.Int64
	if _, err := srv.NewTimerJob("@every 50ms", func() { runs.Add(1) }); err != nil {
		t.Fatalf("NewTimerJob: %v", err)
	}

	runOnce := func() int64 {
		base := runs.Load()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- srv.Start(ctx) }()

		deadline := time.Now().Add(3 * time.Second)
		for runs.Load() == base && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if runs.Load() == base {
			cancel()
			<-done
			t.Fatal("job did not run after start")
		}

		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Start did not return after cancel")
		}

		stopped := runs.Load()
		time.Sleep(200 * time.Millisecond)
		if got := runs.Load(); got != stopped {
			t.Fatalf("job kept running after stop: %d -> %d", stopped, got)
		}
		return stopped
	}

	first := runOnce()
	second := runOnce()
	if second <= first {
		t.Fatalf("second run did not execute job: first=%d second=%d", first, second)
	}
}
